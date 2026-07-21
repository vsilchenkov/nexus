// Package usecase — бизнес-логика Web Service (§17.2 ТЗ).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// NodeUsecase — CRUD над узлами + write-through кеш + audit-log.
//
// Create/Update/Delete атомарны: репо-операция и запись в audit идут одной
// транзакцией через UnitOfWork (§17.4 ТЗ). Если uow=nil — fallback на
// не-транзакционный путь (репо + audit вызываются по отдельности).
//
// defaultTeamID — UUID 'default'-team из миграции 0008. Используется как
// fallback для List и Create, когда caller (handler) не передал team scope.
// В блоке B (team-switcher в сессии) handler начнёт передавать
// current_team_id из сессии, и default останется только для CLI-сценариев.
//
// teams — опциональный TeamRepo (multi-tenancy v2, Phase 10.C.2). Если
// задан, Create/Update нормализуют n.ClickHouseTable до полного имени
// "<team.ch_database>.<table>" — Sender и ch_housekeeping всегда пишут в
// БД конкретной команды независимо от Database в CH-Options. Если nil —
// нормализация выключена (legacy single-team / unit-тесты).
// provisioner — опциональный CH-provisioner (multi-tenancy v2, Phase
// 11.B). Нужен только для Move (RENAME TABLE между БД команд). Если nil
// (Web без ClickHouse) — Move переносит только PG-метаданные.
// templates — опциональный каталог шаблонов CH-таблиц (§19, Phase F1.5).
// Если задан вместе с provisioner'ом, при Create/Update узла с непустым
// ClickHouseTemplateID рендерится DDL и создаётся таблица (CREATE TABLE IF
// NOT EXISTS) в БД команды. Если узел задаёт template_id, но templates или
// provisioner == nil — Create/Update вернут ErrCHUnavailable (не тихий пропуск).
type NodeUsecase struct {
	repo           port.NodeRepo
	cache          port.NodeCache
	audit          *AuditUsecase
	uow            port.UnitOfWork
	teams          port.TeamRepo
	provisioner    port.TeamProvisioner
	templates      port.CHTemplateRepo
	cacheTTL       time.Duration
	nodesHardLimit int
	defaultTeamID  string
	// selfIngressHosts — список своих authority для self-reference валидации
	// target_url (§32.2). Пустой → проверка отключена.
	selfIngressHosts []string
	// events — публикатор событий инвалидации узла (§57). Опционален (nil =
	// no-op): инъекция сеттером, т.к. позиционный параметр затронул бы ~16
	// вызовов NewNodeUsecase (интеграционные тесты).
	events nodeInvalidationPublisher
	// tableUsage — счётчик узлов на одной CH-таблице. Нужен только Move, чтобы
	// не утащить общую таблицу за одним узлом. Опционален по той же причине,
	// что и events (nil → таблица считается personal, прежнее поведение).
	tableUsage port.NodeTableUsage
	logger     logging.Logger
}

// nodeInvalidationPublisher публикует событие «конфиг узла изменился», чтобы
// Receiver выселил его из кешей (§57). Определён на стороне консьюмера (ISP).
type nodeInvalidationPublisher interface {
	PublishNodeChange(ctx context.Context, teamID, path, oldPath string) error
}

// SetInvalidationPublisher инъектирует publisher событий инвалидации (§57).
// Вызывается один раз в main-wiring; nil оставляет публикацию no-op.
func (u *NodeUsecase) SetInvalidationPublisher(p nodeInvalidationPublisher) {
	u.events = p
}

// SetTableUsage инъектирует счётчик узлов на одной CH-таблице (см. Move).
// Вызывается один раз в main-wiring; nil сохраняет прежнее поведение переноса.
func (u *NodeUsecase) SetTableUsage(t port.NodeTableUsage) {
	u.tableUsage = t
}

// publishInvalidate — best-effort уведомление Receiver'а об изменении узла (§57).
// Ошибка логируется, но не эскалируется: узел уже сохранён, TTL кеша — страховка.
func (u *NodeUsecase) publishInvalidate(ctx context.Context, teamID, path, oldPath string) {
	if u.events == nil || path == "" {
		return
	}
	if err := u.events.PublishNodeChange(ctx, teamID, path, oldPath); err != nil {
		u.logger.Warn("node invalidation publish failed",
			u.logger.Str("team_id", teamID), u.logger.Str("path", path), u.logger.Err(err))
		return
	}
	u.logger.Debug("node invalidation published",
		u.logger.Str("team_id", teamID), u.logger.Str("path", path), u.logger.Str("old_path", oldPath))
}

func NewNodeUsecase(
	repo port.NodeRepo,
	cache port.NodeCache,
	audit *AuditUsecase,
	uow port.UnitOfWork,
	teams port.TeamRepo,
	provisioner port.TeamProvisioner,
	templates port.CHTemplateRepo,
	cacheTTL time.Duration,
	nodesHardLimit int,
	defaultTeamID string,
	selfIngressHosts []string,
	logger logging.Logger,
) *NodeUsecase {
	return &NodeUsecase{
		repo:             repo,
		cache:            cache,
		audit:            audit,
		uow:              uow,
		teams:            teams,
		provisioner:      provisioner,
		templates:        templates,
		cacheTTL:         cacheTTL,
		nodesHardLimit:   nodesHardLimit,
		defaultTeamID:    defaultTeamID,
		selfIngressHosts: selfIngressHosts,
		logger:           logger,
	}
}

// provisionTable создаёт таблицу логов узла в ClickHouse (§19.5).
//
//   - Имя таблицы пустое — no-op.
//   - Явно выбран template_id — строгая семантика: рендерим его DDL; при
//     недоступном templates/provisioner возвращаем ErrCHUnavailable (узел
//     нельзя создать «вслепую» с несуществующей таблицей).
//   - template_id пуст, но логирование включено — создаём таблицу из
//     дефолтного шаблона каталога (§19). Иначе чтение логов/метрик упадёт с
//     CH code 60 «Unknown table» (узел в PG ссылается на несуществующую
//     таблицу — баг тестового стенда). Деградация здесь мягкая: при
//     недоступности CH/каталога узел всё равно создаётся, с предупреждением.
//   - Логирование выключено и шаблон не выбран — no-op (legacy/ручная таблица).
//   - external_table — no-op при любых остальных условиях (§64): таблицей
//     владеет оператор или посторонний сервис-писатель, создавать её (и тем
//     более по чужому шаблону) Nexus не вправе.
func (u *NodeUsecase) provisionTable(ctx context.Context, n *domain.Node) error {
	if n.ClickHouseTable == "" {
		return nil
	}

	if n.ExternalTable {
		// §51.9: тихий пропуск управления таблицей должен быть виден на debug —
		// иначе «почему не создалась таблица» выясняется только чтением кода.
		u.logger.Debug("provision skipped: external table",
			u.logger.Str("path", n.Path), u.logger.Str("table", n.ClickHouseTable))
		return nil
	}

	if n.ClickHouseTemplateID != "" {
		if u.templates == nil || u.provisioner == nil {
			return ErrCHUnavailable
		}
		tmpl, err := u.templates.Get(ctx, n.ClickHouseTemplateID)
		if err != nil {
			return err
		}
		return u.createTableFromTemplate(ctx, n, tmpl)
	}

	if !n.LoggingEnabled {
		return nil
	}
	if u.templates == nil || u.provisioner == nil {
		u.logger.Warn("logging enabled but CH provisioner unavailable; node table not created",
			u.logger.Str("path", n.Path), u.logger.Str("table", n.ClickHouseTable))
		return nil
	}
	tmpl, err := u.templates.GetDefault(ctx)
	if err != nil {
		u.logger.Warn("no default CH template; node table not provisioned",
			u.logger.Str("path", n.Path), u.logger.Str("table", n.ClickHouseTable), u.logger.Err(err))
		return nil
	}
	if err := u.createTableFromTemplate(ctx, n, tmpl); err != nil {
		u.logger.Warn("default CH table provisioning failed; node created without table",
			u.logger.Str("path", n.Path), u.logger.Str("table", n.ClickHouseTable), u.logger.Err(err))
		return nil
	}
	return nil
}

// createTableFromTemplate рендерит DDL шаблона для таблицы узла и создаёт её
// (CREATE TABLE IF NOT EXISTS — идемпотентно).
func (u *NodeUsecase) createTableFromTemplate(ctx context.Context, n *domain.Node, tmpl *domain.CHTemplate) error {
	ddl, err := tmpl.RenderCreateTable(n.ClickHouseTable, n.ClickHouseRetentionDays)
	if err != nil {
		return err
	}
	return u.provisioner.CreateTable(ctx, n.ClickHouseTable, ddl)
}

// normalizeCHTable — если ClickHouseTable непустой и не содержит '.',
// префиксует именем БД команды узла. Возвращает безмолвно если teams==nil
// (single-team путь) или imя БД не удалось получить.
func (u *NodeUsecase) normalizeCHTable(ctx context.Context, n *domain.Node) error {
	if u.teams == nil || n.ClickHouseTable == "" || n.TeamID == "" {
		return nil
	}
	if strings.Contains(n.ClickHouseTable, ".") {
		return nil
	}
	t, err := u.teams.GetByID(ctx, n.TeamID)
	if err != nil {
		return fmt.Errorf("resolve team ch_database: %w", err)
	}
	n.ClickHouseTable = t.CHDatabase + "." + n.ClickHouseTable
	return nil
}

// Get возвращает узел и проверяет, что он принадлежит указанной команде
// (multi-tenancy v2). teamID="" пропускает проверку — это legacy-путь для
// CLI/тестов; production handler'ы передают currentTeamID(c).
//
// При несовпадении возвращает ErrNodeNotFound (не утечка существования
// узла в чужой команде).
func (u *NodeUsecase) Get(ctx context.Context, id, teamID string) (*domain.Node, error) {
	n, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	if teamID != "" && n.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	return n, nil
}

// ResolveTeam возвращает команду, которой принадлежит узел, если вызывающий
// пользователь состоит в ней (§58). Нужен для шаринга ссылки на страницу узла:
// фронт узнаёт команду узла ДО загрузки самого узла (team-scoped Get вернул бы
// 404 при несовпадении текущей команды сессии) и авто-переключает сессию на неё.
//
// Единый ErrNodeNotFound на «узла нет» И «пользователь не член команды узла» —
// та же no-leak-семантика, что у Get: существование чужих узлов не утекает
// (§18). Handler маппит его в 404, фронт показывает «Узел не доступен» (§58, п.3).
func (u *NodeUsecase) ResolveTeam(ctx context.Context, userID, nodeID string) (*domain.Team, error) {
	if u.teams == nil {
		return nil, fmt.Errorf("resolve node team: team repo unavailable")
	}
	n, err := u.repo.Get(ctx, nodeID)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			return nil, domain.ErrNodeNotFound
		}
		return nil, fmt.Errorf("resolve node team: get node: %w", err)
	}
	memberships, err := u.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve node team: list memberships: %w", err)
	}
	for _, m := range memberships {
		if m.Team.ID == n.TeamID {
			team := m.Team
			u.logger.Debug("resolve node team: matched",
				u.logger.Str("node_id", nodeID), u.logger.Str("team_id", team.ID))
			return &team, nil
		}
	}
	// Пользователь не состоит в команде узла → узел ему недоступен (§58, п.3).
	// Тихий отказ логируем на debug (§51.9): помогает разобрать «почему у
	// коллеги "Узел не доступен"» без утечки в info/warn.
	u.logger.Debug("resolve node team: user is not a member of node team",
		u.logger.Str("node_id", nodeID), u.logger.Str("node_team_id", n.TeamID),
		u.logger.Str("user_id", userID))
	return nil, domain.ErrNodeNotFound
}

func (u *NodeUsecase) List(ctx context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	if f.TeamID == "" {
		f.TeamID = u.defaultTeamID
	}
	return u.repo.List(ctx, f)
}

// NodeSearchHit — узел, найденный кросс-командным поиском (§62), с командой-
// владельцем (для бейджа в выпадающем списке и авто-переключения).
type NodeSearchHit struct {
	Node     *domain.Node
	TeamSlug string
	TeamName string
}

// minSearchQueryRunesNode — нижняя граница длины запроса кросс-командного поиска
// (§62): короче — сразу пусто (не грузим все команды ILIKE'ом по одной-двум
// буквам). Совпадает с minSearchQueryRunes истории (auth.go).
const minSearchQueryRunesNode = 2

const (
	defaultNodeSearchLimit = 20
	maxNodeSearchLimit     = 50
)

// SearchAcrossTeams — поиск узлов по всем командам пользователя (§62): ILIKE по
// path/target_url в пределах его членств. Возвращает узлы с командой-владельцем.
//
// Обход членств — как в ResolveTeam: TeamRepo обязателен; репозиторий узлов
// фильтрует по team_id = ANY(<членства>) (port.ListNodesFilter.TeamIDs), имена
// команд обогащаются из уже полученного ListUserTeams (без лишних запросов).
// Пустой результат (короткий запрос, нет членств) — не ошибка.
func (u *NodeUsecase) SearchAcrossTeams(ctx context.Context, userID, query string, limit int) ([]NodeSearchHit, error) {
	if u.teams == nil {
		return nil, fmt.Errorf("search nodes: team repo unavailable")
	}
	q := strings.TrimSpace(query)
	if len([]rune(q)) < minSearchQueryRunesNode {
		return nil, nil
	}
	switch {
	case limit <= 0:
		limit = defaultNodeSearchLimit
	case limit > maxNodeSearchLimit:
		limit = maxNodeSearchLimit
	}
	memberships, err := u.teams.ListUserTeams(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("search nodes: list memberships: %w", err)
	}
	if len(memberships) == 0 {
		return nil, nil
	}
	teamIDs := make([]string, 0, len(memberships))
	teamByID := make(map[string]domain.Team, len(memberships))
	for _, m := range memberships {
		teamIDs = append(teamIDs, m.Team.ID)
		teamByID[m.Team.ID] = m.Team
	}
	nodes, err := u.repo.List(ctx, port.ListNodesFilter{
		TeamIDs: teamIDs,
		Search:  q,
		Limit:   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("search nodes: list: %w", err)
	}
	hits := make([]NodeSearchHit, 0, len(nodes))
	for _, n := range nodes {
		tm := teamByID[n.TeamID] // всегда есть: узел отфильтрован по членствам
		hits = append(hits, NodeSearchHit{Node: n, TeamSlug: tm.Slug, TeamName: tm.Name})
	}
	u.logger.Debug("search nodes across teams",
		u.logger.Str("user_id", userID), u.logger.Int("teams", len(teamIDs)),
		u.logger.Int("hits", len(hits)), u.logger.Int("limit", limit))
	return hits, nil
}

// prepareNewNode — общий пайплайн подготовки узла перед вставкой как нового
// (Create и Copy, §53): дефолты, сброс снимка allowlist, нормализация под
// RootMethod и CH-таблицу, валидация, self-reference, hard-limit и
// провижининг CH-таблицы. Мутирует n; возвращает имена молча сброшенных
// несовместимых полей (для audit, §27.6).
func (u *NodeUsecase) prepareNewNode(ctx context.Context, n *domain.Node) ([]string, error) {
	n.SetDefaults()
	// §23: allowlist хостов — производный снимок каталога (node_allowed_hosts).
	// Новый узел создаётся с пустым allowlist; паттерны привязываются отдельно
	// через POST /api/nodes/:id/allowed-hosts. Тело узла снимок не задаёт.
	n.URLAllowedHosts = []string{}
	if n.TeamID == "" {
		n.TeamID = u.defaultTeamID
	}
	// §27.6: для pull-узлов молча сбрасываем несовместимые поля (incoming-auth,
	// url_mode=from_request, динамическая исходящая авторизация).
	cleared := n.NormalizeForRootMethod()
	if err := u.normalizeCHTable(ctx, n); err != nil {
		return nil, err
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	// §32.2: target_url не должен указывать на собственный ingress шины.
	if err := u.checkSelfReference(n); err != nil {
		return nil, err
	}

	// §3.3 ТЗ: hard-limit nodes_hard_limit. Считаем вне транзакции —
	// небольшая гонка возможна, но допустима: финальная сериализация
	// гарантируется уникальным path в БД.
	count, err := u.repo.Count(ctx, n.TeamID)
	if err != nil {
		return nil, fmt.Errorf("count nodes for limit check: %w", err)
	}
	if u.nodesHardLimit > 0 && count >= u.nodesHardLimit {
		return nil, domain.ErrLimitReached
	}

	// §19.5: создаём CH-таблицу из шаблона ДО PG-commit. CREATE TABLE
	// идемпотентен (IF NOT EXISTS); при падении узел не создаётся.
	if err := u.provisionTable(ctx, n); err != nil {
		return nil, err
	}
	return cleared, nil
}

func (u *NodeUsecase) Create(ctx context.Context, actor Actor, n *domain.Node) error {
	// §63: автор создания узла.
	n.CreatedBy = actor.UserLogin
	cleared, err := u.prepareNewNode(ctx, n)
	if err != nil {
		return err
	}

	auditDetails := map[string]any{
		"path":        n.Path,
		"root_method": string(n.RootMethod),
		"target_url":  n.TargetURL,
		"url_mode":    string(n.URLMode),
		"auth_type":   string(n.AuthType),
		"status":      string(n.Status),
	}
	if len(cleared) > 0 {
		auditDetails["cleared_incompatible_fields"] = cleared
	}

	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Create(ctx, n); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeCreate, "node", n.ID, auditDetails))
		}); err != nil {
			return err
		}
	} else {
		if err := u.repo.Create(ctx, n); err != nil {
			return err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeCreate, "node", n.ID, auditDetails)
	}

	u.cacheSet(ctx, n, "create")
	u.publishInvalidate(ctx, n.TeamID, n.Path, "")
	return nil
}

// cacheTeamSlug резолвит slug команды узла для ключа кеша (§50). Ключ Receiver'а —
// "node:<team_slug>:<path>", а в usecase на руках только team_id (UUID).
//
// Пустая строка на выходе = «не трогать кеш»: писать/чистить ключ чужой команды
// хуже, чем подождать TTL (redis.node_ttl_sec) — иначе конфиг узла одной команды
// затрёт кеш одноимённого пути в другой (после §18 path уникален лишь внутри
// команды).
func (u *NodeUsecase) cacheTeamSlug(ctx context.Context, teamID string) string {
	return resolveCacheTeamSlug(ctx, u.teams, teamID, u.logger)
}

// resolveCacheTeamSlug — общий резолв team_id → slug для ключа кеша узла (§50).
// Используют NodeUsecase и HostAllowlistUsecase: обе пишут один и тот же ключ.
func resolveCacheTeamSlug(ctx context.Context, teams port.TeamRepo, teamID string, logger logging.Logger) string {
	if teamID == "" {
		return domain.DefaultTeamSlug
	}
	if teams == nil {
		logger.Warn("node cache: TeamRepo is nil, skip cache op",
			logger.Str("team_id", teamID))
		return ""
	}
	t, err := teams.GetByID(ctx, teamID)
	if err != nil {
		logger.Warn("node cache: resolve team slug failed, skip cache op",
			logger.Str("team_id", teamID), logger.Err(err))
		return ""
	}
	return t.Slug
}

// cacheSet — write-through записи узла в кеш (§9.2). Ошибки не эскалируются:
// узел уже сохранён в PG, кеш догонит по TTL. op — для сообщения в логе.
func (u *NodeUsecase) cacheSet(ctx context.Context, n *domain.Node, op string) {
	slug := u.cacheTeamSlug(ctx, n.TeamID)
	if slug == "" {
		return
	}
	if err := u.cache.Set(ctx, slug, n, u.cacheTTL); err != nil {
		u.logger.Warn("cache set after "+op+" failed",
			u.logger.Str("team", slug), u.logger.Str("path", n.Path), u.logger.Err(err))
		return
	}
	// §51.9 (грабли §50): «какой slug реально ушёл в ключ» — частая причина
	// «инвалидировали не тот кеш»; успешный write-through виден на debug.
	u.logger.Debug("node cache: set",
		u.logger.Str("team", slug), u.logger.Str("path", n.Path), u.logger.Str("op", op))
}

// cacheInvalidate — сброс ключа узла (teamID — команда, которой принадлежит путь).
func (u *NodeUsecase) cacheInvalidate(ctx context.Context, teamID, path, op string) {
	slug := u.cacheTeamSlug(ctx, teamID)
	if slug == "" {
		return
	}
	if err := u.cache.InvalidateByPath(ctx, slug, path); err != nil {
		u.logger.Warn("cache invalidate after "+op+" failed",
			u.logger.Str("team", slug), u.logger.Str("path", path), u.logger.Err(err))
		return
	}
	u.logger.Debug("node cache: invalidated",
		u.logger.Str("team", slug), u.logger.Str("path", path), u.logger.Str("op", op))
}

// Update обновляет узел. teamID — scope multi-tenancy v2; при несовпадении
// с old.TeamID возвращает ErrNodeNotFound. Цепочка handler → currentTeamID(c)
// гарантирует, что нельзя обновить узел чужой команды.
//
// Перенос узла между командами через Update запрещён: handler перед
// вызовом обнуляет n.TeamID = existing.TeamID, плюс здесь явная проверка
// n.TeamID == old.TeamID на случай прямого вызова.
func (u *NodeUsecase) Update(ctx context.Context, actor Actor, n *domain.Node, teamID string) error {
	n.SetDefaults()
	cleared := n.NormalizeForRootMethod() // §27.6
	if err := u.normalizeCHTable(ctx, n); err != nil {
		return err
	}
	if err := n.Validate(); err != nil {
		return err
	}
	// §32.2: target_url не должен указывать на собственный ingress шины.
	if err := u.checkSelfReference(n); err != nil {
		return err
	}
	old, err := u.repo.Get(ctx, n.ID)
	if err != nil {
		return err
	}
	if teamID != "" && old.TeamID != teamID {
		return domain.ErrNodeNotFound
	}
	if n.TeamID != old.TeamID {
		return domain.ErrPermissionDenied
	}
	// §23: снимок allowlist хостов управляется только каталогом (link/unlink) —
	// сохраняем существующий, чтобы PUT узла его не затирал.
	n.URLAllowedHosts = old.URLAllowedHosts
	// §63: автор последнего изменения; created_by приходит из БД (scan old не
	// используем — Update по SET не трогает created_by), поэтому переносим его с
	// исходного узла, чтобы ответ содержал верного создателя.
	n.CreatedBy = old.CreatedBy
	n.UpdatedBy = actor.UserLogin
	// §19.5: (пере)создаём таблицу при смене имени/шаблона либо при включении
	// логирования на узле, у которого таблицы ещё не было.
	if old.ClickHouseTable != n.ClickHouseTable ||
		old.ClickHouseTemplateID != n.ClickHouseTemplateID ||
		(n.LoggingEnabled && !old.LoggingEnabled) {
		if err := u.provisionTable(ctx, n); err != nil {
			return err
		}
	}
	diff := diffNodes(old, n)
	if len(cleared) > 0 {
		diff["cleared_incompatible_fields"] = cleared
	}
	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Update(ctx, n); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeUpdate, "node", n.ID, diff))
		}); err != nil {
			return err
		}
	} else {
		if err := u.repo.Update(ctx, n); err != nil {
			return err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeUpdate, "node", n.ID, diff)
	}

	if old.Path != n.Path {
		// Команда при Update не меняется (проверка выше) — старый путь чистим
		// в той же команде.
		u.cacheInvalidate(ctx, old.TeamID, old.Path, "update (old path)")
	}
	u.cacheSet(ctx, n, "update")
	// §57: гарантированная инвалидация в Receiver. При переименовании передаём и
	// старый путь — иначе старый ключ ещё TTL указывал бы на переехавший узел.
	oldPath := ""
	if old.Path != n.Path {
		oldPath = old.Path
	}
	u.publishInvalidate(ctx, n.TeamID, n.Path, oldPath)
	return nil
}

// SetStatus меняет ТОЛЬКО статус узла (§35) — лёгкая альтернатива полному Update
// для кнопок «Пауза»/«Отключить». Узел читается из репо (с расшифрованными
// кредами) и сохраняется как есть, меняется лишь Status — прочие поля/креды/
// allowlist-снимок не трогаются, таблица CH не перепровижинится.
func (u *NodeUsecase) SetStatus(ctx context.Context, actor Actor, id, teamID string, status domain.NodeStatus) error {
	if !status.Valid() {
		return domain.ErrNodeInvalidStatus
	}
	old, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if teamID != "" && old.TeamID != teamID {
		return domain.ErrNodeNotFound
	}
	if old.Status == status {
		return nil // no-op
	}
	updated := *old
	updated.Status = status
	updated.UpdatedBy = actor.UserLogin // §63
	diff := map[string]any{"status": map[string]string{"before": string(old.Status), "after": string(status)}}
	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Update(ctx, &updated); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeUpdate, "node", id, diff))
		}); err != nil {
			return err
		}
	} else {
		if err := u.repo.Update(ctx, &updated); err != nil {
			return err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeUpdate, "node", id, diff)
	}
	u.cacheSet(ctx, &updated, "status change")
	u.publishInvalidate(ctx, updated.TeamID, updated.Path, "")
	return nil
}

// Delete удаляет узел. teamID — scope multi-tenancy v2.
func (u *NodeUsecase) Delete(ctx context.Context, actor Actor, id, teamID string) error {
	n, err := u.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			return err
		}
		return fmt.Errorf("get before delete: %w", err)
	}
	if teamID != "" && n.TeamID != teamID {
		return domain.ErrNodeNotFound
	}
	details := map[string]any{
		"path":             n.Path,
		"clickhouse_table": n.ClickHouseTable,
	}
	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Delete(ctx, id); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeDelete, "node", n.ID, details))
		}); err != nil {
			return err
		}
	} else {
		if err := u.repo.Delete(ctx, id); err != nil {
			return err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeDelete, "node", n.ID, details)
	}

	u.cacheInvalidate(ctx, n.TeamID, n.Path, "delete")
	u.publishInvalidate(ctx, n.TeamID, n.Path, "")
	return nil
}

// MovePreview — что случится с таблицей логов при переносе узла в команду
// targetTeamSlug. Считается ДО переноса, чтобы предупредить в UI: у операции
// два неочевидных исхода, и оба меняют то, какие логи узел покажет дальше.
type MovePreview struct {
	// TargetTable — имя таблицы узла после переноса ("<db>.<table>").
	TargetTable string `json:"target_table"`
	// TableShared — исходную таблицу делят другие узлы: она останется у текущей
	// команды вместе с историей, а узлу достанется отдельная таблица.
	TableShared bool `json:"table_shared"`
	// SharedWith — сколько ДРУГИХ узлов на исходной таблице (0, если личная).
	SharedWith int `json:"shared_with"`
	// TargetTableExists — в целевой команде уже есть таблица с этим именем:
	// узел подключится к ней и увидит записи, которые писал не он.
	TargetTableExists bool `json:"target_table_exists"`
}

// MovePreview собирает предпросмотр переноса (см. MovePreview).
// Проверки best-effort: недоступность ClickHouse не должна блокировать
// открытие диалога — в этом случае поле TargetTableExists остаётся false.
func (u *NodeUsecase) MovePreview(ctx context.Context, nodeID, currentTeamID, targetTeamSlug string) (*MovePreview, error) {
	n, err := u.repo.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if currentTeamID != "" && n.TeamID != currentTeamID {
		return nil, domain.ErrNodeNotFound
	}
	if u.teams == nil {
		return nil, fmt.Errorf("move preview requires TeamRepo")
	}
	target, err := u.teams.GetBySlug(ctx, targetTeamSlug)
	if err != nil {
		return nil, err
	}
	if target.ID == n.TeamID {
		return nil, domain.ErrPermissionDenied
	}

	newTable := targetCHTable(n, target.CHDatabase)
	res := &MovePreview{TargetTable: newTable}
	if n.ClickHouseTable == "" {
		return res, nil
	}

	res.SharedWith = u.countCHTableSiblings(ctx, n.ClickHouseTable, n.ID)
	res.TableShared = res.SharedWith > 0

	if u.provisioner != nil && newTable != "" && newTable != n.ClickHouseTable {
		exists, err := u.provisioner.TableExists(ctx, newTable)
		if err != nil {
			u.logger.Warn("move preview: target table check failed",
				u.logger.Str("table", newTable), u.logger.Err(err))
		} else {
			res.TargetTableExists = exists
		}
	}
	u.logger.Debug("move preview built",
		u.logger.Str("path", n.Path), u.logger.Str("target_table", newTable),
		u.logger.Int("shared_with", res.SharedWith))
	return res, nil
}

// Move переносит узел в другую команду (multi-tenancy v2, Phase 11.B).
//
// PG-запись авторитетна: в одной UoW-транзакции меняются team_id и
// clickhouse_table (БД-префикс → новая команда) + audit. Конфликт пути
// в целевой команде (UNIQUE(team_id, path)) → ErrNodeAlreadyExists.
// После commit'а — best-effort RENAME TABLE в ClickHouse (логи следуют
// за узлом); если исходной таблицы нет или provisioner отсутствует —
// CH-операция пропускается без ошибки.
//
// currentTeamID — scope (узел чужой команды → ErrNodeNotFound).
func (u *NodeUsecase) Move(ctx context.Context, actor Actor, nodeID, currentTeamID, targetTeamSlug string) error {
	n, err := u.repo.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if currentTeamID != "" && n.TeamID != currentTeamID {
		return domain.ErrNodeNotFound
	}
	if u.teams == nil {
		return fmt.Errorf("move requires TeamRepo")
	}
	target, err := u.teams.GetBySlug(ctx, targetTeamSlug)
	if err != nil {
		return err
	}
	if target.ID == n.TeamID {
		return domain.ErrPermissionDenied // перенос в ту же команду — no-op
	}

	oldTable := n.ClickHouseTable
	newTable := targetCHTable(n, target.CHDatabase)

	moved := *n
	moved.TeamID = target.ID
	moved.ClickHouseTable = newTable
	moved.UpdatedBy = actor.UserLogin // §63

	details := map[string]any{
		"path":          n.Path,
		"from_team":     n.TeamID,
		"to_team":       target.ID,
		"to_team_slug":  target.Slug,
		"from_ch_table": oldTable,
		"to_ch_table":   newTable,
	}

	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Update(ctx, &moved); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeMove, "node", n.ID, details))
		}); err != nil {
			return err
		}
	} else {
		if err := u.repo.Update(ctx, &moved); err != nil {
			return err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeMove, "node", n.ID, details)
	}

	// CH-таблица — best-effort: PG уже авторитетно указывает на новую БД.
	if u.provisioner != nil && oldTable != "" && newTable != "" && oldTable != newTable {
		u.relocateCHTable(ctx, &moved, oldTable, newTable)
	}

	// Чистим ключ ИСХОДНОЙ команды (n.TeamID — до переноса): иначе трафик по
	// старому пути ещё TTL шёл бы на уехавший узел. Ключ целевой команды
	// наполнит сам Receiver при первом запросе (write-back).
	u.cacheInvalidate(ctx, n.TeamID, n.Path, "move")
	// §57: путь не меняется, меняется команда — выселяем и исходную (n.TeamID),
	// и целевую (moved.TeamID) команду; Receiver наполнит целевую при первом запросе.
	u.publishInvalidate(ctx, n.TeamID, n.Path, "")
	u.publishInvalidate(ctx, moved.TeamID, n.Path, "")
	return nil
}

// relocateCHTable переносит таблицу логов вслед за узлом — best-effort, узел
// в PG уже перенесён.
//
// Таблицу логов могут делить НЕСКОЛЬКО узлов (типовой случай — семейство узлов
// одного сервиса с общим clickhouse_table). Безусловный RENAME утаскивал такую
// таблицу за одним переехавшим узлом, и все остальные оставались со ссылкой на
// несуществующую таблицу: логирование у них тихо ломалось, а CH отвечал «code
// 60, Unknown table» (поймано на стенде: перенос одного узла из 176 обезглавил
// остальные 175). Поэтому общая таблица остаётся у прежней команды вместе с
// историей, а переехавшему узлу создаётся собственная таблица в её БД.
func (u *NodeUsecase) relocateCHTable(ctx context.Context, moved *domain.Node, oldTable, newTable string) {
	if shared := u.countCHTableSiblings(ctx, oldTable, moved.ID); shared > 0 {
		u.logger.Info("node move: CH table is shared, keeping it in place",
			u.logger.Str("table", oldTable), u.logger.Str("path", moved.Path),
			u.logger.Int("other_nodes", shared))
		// Провижининг идёт через CREATE TABLE IF NOT EXISTS, поэтому «создать
		// свою таблицу» превращается в «подключиться к чужой», если имя в
		// целевой БД уже занято. Молчать об этом нельзя: узел сразу покажет
		// записи, которых он не писал (ровно так «появились» счётчики у
		// переехавшего узла на стенде).
		u.warnIfTargetTableExists(ctx, moved, newTable)
		// Новая таблица по шаблону узла. Мягкая деградация: узел уже перенесён,
		// а таблицу дотянет provisionTable при следующем сохранении узла.
		if err := u.provisionTable(ctx, moved); err != nil {
			u.logger.Warn("node move: new CH table provisioning failed",
				u.logger.Str("table", newTable), u.logger.Str("path", moved.Path),
				u.logger.Err(err))
		}
		return
	}

	err := u.provisioner.RenameTable(ctx, oldTable, newTable)
	switch {
	case err == nil:
	case errors.Is(err, port.ErrSourceTableAbsent):
		u.logger.Info("node move: source CH table absent, skip rename",
			u.logger.Str("from", oldTable))
	case errors.Is(err, port.ErrTargetTableExists):
		// В целевой команде уже есть таблица с таким именем — узел будет
		// писать в неё; исходная остаётся с прежней историей.
		u.logger.Info("node move: target CH table exists, skip rename",
			u.logger.Str("from", oldTable), u.logger.Str("to", newTable))
	default:
		u.logger.Warn("node move: CH rename failed (PG already moved)",
			u.logger.Str("from", oldTable), u.logger.Str("to", newTable),
			u.logger.Err(err))
	}
}

// warnIfTargetTableExists предупреждает, что узел подключается к УЖЕ
// существующей таблице целевой команды, а не получает пустую. Best-effort:
// недоступный ClickHouse не должен мешать переносу, который уже произошёл.
func (u *NodeUsecase) warnIfTargetTableExists(ctx context.Context, moved *domain.Node, newTable string) {
	if u.provisioner == nil || newTable == "" {
		return
	}
	exists, err := u.provisioner.TableExists(ctx, newTable)
	if err != nil {
		u.logger.Debug("node move: target CH table check failed",
			u.logger.Str("table", newTable), u.logger.Err(err))
		return
	}
	if exists {
		u.logger.Warn("node move: target CH table already exists, node will read and write its data",
			u.logger.Str("table", newTable), u.logger.Str("path", moved.Path))
	}
}

// countCHTableSiblings — сколько ДРУГИХ узлов ссылаются на ту же таблицу.
// Порт опционален (legacy-wiring/unit-тесты) и запрос может упасть — в обоих
// случаях возвращаем 0, т.е. прежнее поведение (RENAME): молча оставлять узел
// без таблицы хуже, чем перенести её.
func (u *NodeUsecase) countCHTableSiblings(ctx context.Context, table, nodeID string) int {
	if u.tableUsage == nil {
		u.logger.Debug("node move: table usage port unavailable, assuming exclusive table",
			u.logger.Str("table", table))
		return 0
	}
	n, err := u.tableUsage.CountByCHTable(ctx, table, nodeID)
	if err != nil {
		u.logger.Warn("node move: count nodes by CH table failed, assuming exclusive table",
			u.logger.Str("table", table), u.logger.Err(err))
		return 0
	}
	u.logger.Debug("node move: CH table usage counted",
		u.logger.Str("table", table), u.logger.Int("other_nodes", n))
	return n
}

// rebaseCHTable меняет БД-префикс полного имени "<db>.<table>" на newDB.
// Если имя без точки (legacy) — префиксует newDB. Пустое имя остаётся
// пустым.
// targetCHTable — имя таблицы логов узла после переноса в команду с БД newDB.
// Внешняя таблица (§64) имя НЕ меняет: ею владеет оператор или посторонний
// сервис-писатель, и она не обязана лежать в БД команды. Ребейз увёл бы узел на
// несуществующее имя в чужой БД, а сама таблица осталась бы без читателя.
func targetCHTable(n *domain.Node, newDB string) string {
	if n.ExternalTable {
		return n.ClickHouseTable
	}
	return rebaseCHTable(n.ClickHouseTable, newDB)
}

func rebaseCHTable(full, newDB string) string {
	if full == "" {
		return ""
	}
	if _, after, ok := strings.Cut(full, "."); ok {
		return newDB + "." + after
	}
	return newDB + "." + full
}

// diffNodes — упрощённый diff основных полей. Полный diff с двумя
// колонками «до/после» (для UI) — Phase 3.
func diffNodes(old, n *domain.Node) map[string]any {
	d := map[string]any{}
	add := func(k string, before, after any) {
		if before != after {
			d[k] = map[string]any{"before": before, "after": after}
		}
	}
	add("path", old.Path, n.Path)
	add("target_url", old.TargetURL, n.TargetURL)
	add("url_mode", string(old.URLMode), string(n.URLMode))
	add("auth_type", string(old.AuthType), string(n.AuthType))
	add("incoming_auth_type", string(old.IncomingAuthType), string(n.IncomingAuthType))
	add("status", string(old.Status), string(n.Status))
	add("timeout_ms", old.TimeoutMs, n.TimeoutMs)
	add("retry_count", old.RetryCount, n.RetryCount)
	add("webhook_signature_header", old.WebhookSignatureHeader, n.WebhookSignatureHeader)
	add("webhook_signature_prefix", old.WebhookSignaturePrefix, n.WebhookSignaturePrefix)
	// §64: переключение внешней таблицы меняет, управляет ли Nexus схемой и
	// retention этой таблицы, — такое решение должно быть видно в аудите.
	add("external_table", old.ExternalTable, n.ExternalTable)
	if old.AuthCredentials != n.AuthCredentials {
		d["auth_credentials"] = "changed"
	}
	if old.IncomingAuthCredentials != n.IncomingAuthCredentials {
		d["incoming_auth_credentials"] = "changed"
	}
	// §27: RabbitMQAsync-поля (rmq_password маскируется, как остальные креды).
	add("rmq_host", old.RMQHost, n.RMQHost)
	add("rmq_port", old.RMQPort, n.RMQPort)
	add("rmq_vhost", old.RMQVHost, n.RMQVHost)
	add("rmq_user", old.RMQUser, n.RMQUser)
	add("rmq_queue", old.RMQQueue, n.RMQQueue)
	add("rmq_use_tls", old.RMQUseTLS, n.RMQUseTLS)
	add("pull_interval_sec", old.PullIntervalSec, n.PullIntervalSec)
	add("pull_batch_size", old.PullBatchSize, n.PullBatchSize)
	add("pull_prefetch", old.PullPrefetch, n.PullPrefetch)
	if old.RMQPassword != n.RMQPassword {
		d["rmq_password"] = "changed"
	}
	return d
}
