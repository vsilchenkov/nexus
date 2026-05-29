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
	logger         logging.Logger
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
	logger logging.Logger,
) *NodeUsecase {
	return &NodeUsecase{
		repo:           repo,
		cache:          cache,
		audit:          audit,
		uow:            uow,
		teams:          teams,
		provisioner:    provisioner,
		templates:      templates,
		cacheTTL:       cacheTTL,
		nodesHardLimit: nodesHardLimit,
		defaultTeamID:  defaultTeamID,
		logger:         logger,
	}
}

// provisionTable создаёт таблицу логов узла из выбранного шаблона (§19.5).
// No-op если template_id пуст (ручная/legacy таблица) или имя таблицы пустое.
// Если template_id задан, но templates/provisioner недоступны — ErrCHUnavailable.
func (u *NodeUsecase) provisionTable(ctx context.Context, n *domain.Node) error {
	if n.ClickHouseTemplateID == "" || n.ClickHouseTable == "" {
		return nil
	}
	if u.templates == nil || u.provisioner == nil {
		return ErrCHUnavailable
	}
	tmpl, err := u.templates.Get(ctx, n.ClickHouseTemplateID)
	if err != nil {
		return err
	}
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

func (u *NodeUsecase) List(ctx context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	if f.TeamID == "" {
		f.TeamID = u.defaultTeamID
	}
	return u.repo.List(ctx, f)
}

func (u *NodeUsecase) Create(ctx context.Context, actor Actor, n *domain.Node) error {
	n.SetDefaults()
	if n.TeamID == "" {
		n.TeamID = u.defaultTeamID
	}
	if err := u.normalizeCHTable(ctx, n); err != nil {
		return err
	}
	if err := n.Validate(); err != nil {
		return err
	}

	// §3.3 ТЗ: hard-limit nodes_hard_limit. Считаем вне транзакции —
	// небольшая гонка возможна, но допустима: финальная сериализация
	// гарантируется уникальным path в БД.
	count, err := u.repo.Count(ctx, n.TeamID)
	if err != nil {
		return fmt.Errorf("count nodes for limit check: %w", err)
	}
	if u.nodesHardLimit > 0 && count >= u.nodesHardLimit {
		return domain.ErrLimitReached
	}

	// §19.5: создаём CH-таблицу из шаблона ДО PG-commit. CREATE TABLE
	// идемпотентен (IF NOT EXISTS); при падении узел не создаётся.
	if err := u.provisionTable(ctx, n); err != nil {
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

	if err := u.cache.Set(ctx, n, u.cacheTTL); err != nil {
		u.logger.Warn("cache set after create failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
	return nil
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
	if err := u.normalizeCHTable(ctx, n); err != nil {
		return err
	}
	if err := n.Validate(); err != nil {
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
	// §19.5: пересоздаём таблицу только если сменились имя или шаблон.
	if old.ClickHouseTable != n.ClickHouseTable || old.ClickHouseTemplateID != n.ClickHouseTemplateID {
		if err := u.provisionTable(ctx, n); err != nil {
			return err
		}
	}
	diff := diffNodes(old, n)
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
		if err := u.cache.InvalidateByPath(ctx, old.Path); err != nil {
			u.logger.Warn("cache invalidate old path failed",
				u.logger.Str("old_path", old.Path), u.logger.Err(err))
		}
	}
	if err := u.cache.Set(ctx, n, u.cacheTTL); err != nil {
		u.logger.Warn("cache set after update failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
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

	if err := u.cache.InvalidateByPath(ctx, n.Path); err != nil {
		u.logger.Warn("cache invalidate after delete failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
	return nil
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
	newTable := rebaseCHTable(oldTable, target.CHDatabase)

	moved := *n
	moved.TeamID = target.ID
	moved.ClickHouseTable = newTable

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

	// CH RENAME — best-effort: PG уже авторитетно указывает на новую БД.
	if u.provisioner != nil && oldTable != "" && newTable != "" && oldTable != newTable {
		if err := u.provisioner.RenameTable(ctx, oldTable, newTable); err != nil {
			if errors.Is(err, port.ErrSourceTableAbsent) {
				u.logger.Info("node move: source CH table absent, skip rename",
					u.logger.Str("from", oldTable))
			} else {
				u.logger.Warn("node move: CH rename failed (PG already moved)",
					u.logger.Str("from", oldTable), u.logger.Str("to", newTable),
					u.logger.Err(err))
			}
		}
	}

	if err := u.cache.InvalidateByPath(ctx, n.Path); err != nil {
		u.logger.Warn("cache invalidate after move failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
	return nil
}

// rebaseCHTable меняет БД-префикс полного имени "<db>.<table>" на newDB.
// Если имя без точки (legacy) — префиксует newDB. Пустое имя остаётся
// пустым.
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
	if old.AuthCredentials != n.AuthCredentials {
		d["auth_credentials"] = "changed"
	}
	if old.IncomingAuthCredentials != n.IncomingAuthCredentials {
		d["incoming_auth_credentials"] = "changed"
	}
	return d
}
