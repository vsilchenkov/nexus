package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// RejectedUsecase — чтение журнала отказов на входе (§94.6).
type RejectedUsecase struct {
	repo      port.RejectedRepo
	teams     port.TeamRepo
	nodes     port.NodeRepo
	audit     *AuditUsecase
	retention *RejectRetentionProvider
	clock     clock.Clock
	logger    logging.Logger
}

// RejectedOption — функциональная опция конструктора.
type RejectedOption func(*RejectedUsecase)

// WithRejectedClock подменяет источник времени (§4 CLAUDE.md): от него зависит
// отметка «разобрано».
func WithRejectedClock(c clock.Clock) RejectedOption {
	return func(u *RejectedUsecase) {
		if c != nil {
			u.clock = c
		}
	}
}

func NewRejectedUsecase(repo port.RejectedRepo, teams port.TeamRepo, nodes port.NodeRepo,
	audit *AuditUsecase, retention *RejectRetentionProvider, logger logging.Logger,
	opts ...RejectedOption,
) *RejectedUsecase {
	u := &RejectedUsecase{
		repo: repo, teams: teams, nodes: nodes, audit: audit,
		retention: retention, clock: clock.System(), logger: logger,
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// RejectedScope — кто спрашивает (§94.6).
//
// Оператор и менеджер видят журнал СВОЕЙ текущей команды, администратор — все
// команды и группы с неопознанным слогом (обращения по адресу несуществующей
// команды). Это не то же самое, что скоуп аудита по членствам: журнал
// привязан к слогу из URL, а не к команде-владельцу записи.
type RejectedScope struct {
	// TeamID — команда сессии. Пусто у администратора без выбранной команды.
	TeamID string
	// IsAdmin снимает ограничение по команде.
	IsAdmin bool
	// TeamSlug — необязательный фильтр администратора («показать команду X»).
	// У остальных ролей игнорируется: их скоуп задаёт сессия.
	TeamSlug string
	// UnknownTeamOnly — только группы, чей слог не соответствует ни одной
	// команде (администратор).
	UnknownTeamOnly bool
}

// RejectedList — страница выдачи вместе с общим числом и состоянием журнала.
type RejectedList struct {
	Groups []*domain.RejectedGroup
	Total  int64
	// RetentionDays и Collecting описывают состояние журнала: без них интерфейс
	// не отличит «отказов не было» от «сбор выключен настройкой».
	RetentionDays int
	Collecting    bool
}

// RejectedDetails — карточка группы.
type RejectedDetails struct {
	Group   *domain.RejectedGroup
	Clients []domain.RejectedClient
	Samples []domain.RejectedSample
	// Suggestion — ближайший существующий узел, если путь похож на опечатку.
	Suggestion *RejectedSuggestion
}

// RejectedSuggestion — подсказка «похоже, имелся в виду этот узел» (§94.6).
type RejectedSuggestion struct {
	NodeID   string
	Path     string
	TeamSlug string
	Distance int
}

// List возвращает страницу групп и общее число под фильтром.
func (u *RejectedUsecase) List(ctx context.Context, sc RejectedScope, f port.RejectedFilter) (RejectedList, error) {
	out := RejectedList{
		RetentionDays: u.retention.Days(),
		Collecting:    u.retention.Enabled(),
	}
	scoped, err := u.applyScope(ctx, sc, f)
	if err != nil {
		return out, err
	}
	groups, err := u.repo.ListRejected(ctx, scoped)
	if err != nil {
		return out, fmt.Errorf("rejected list: %w", err)
	}
	total, err := u.repo.CountRejected(ctx, scoped)
	if err != nil {
		return out, fmt.Errorf("rejected count: %w", err)
	}
	out.Groups = groups
	out.Total = total
	return out, nil
}

// RejectedSummary — счётчики за период плюс состояние журнала.
//
// Состояние едет вместе со счётчиками, а не отдельным запросом: строка на
// рабочем столе спрашивает сводку на каждом тике автообновления, и второй
// поход в PostgreSQL ради двух чисел из памяти ничем не оправдан.
type RejectedSummary struct {
	port.RejectedSummary
	RetentionDays int
	Collecting    bool
}

// Summary — счётчики за период (строка на рабочем столе, бейдж раздела).
func (u *RejectedUsecase) Summary(ctx context.Context, sc RejectedScope, f port.RejectedFilter) (RejectedSummary, error) {
	out := RejectedSummary{
		RetentionDays: u.retention.Days(),
		Collecting:    u.retention.Enabled(),
	}
	scoped, err := u.applyScope(ctx, sc, f)
	if err != nil {
		return out, err
	}
	// Сводка считает и разобранные группы: «сколько всего отказов за сутки» не
	// должно меняться от того, что кто-то нажал «разобрано». Неразобранные
	// приезжают отдельным полем — по нему светится бейдж.
	scoped.IncludeResolved = true
	s, err := u.repo.SummaryRejected(ctx, scoped)
	if err != nil {
		return out, fmt.Errorf("rejected summary: %w", err)
	}
	out.RejectedSummary = s
	return out, nil
}

// Details возвращает карточку группы с клиентами, сэмплами и подсказкой.
func (u *RejectedUsecase) Details(ctx context.Context, sc RejectedScope, id string) (RejectedDetails, error) {
	g, err := u.get(ctx, sc, id)
	if err != nil {
		return RejectedDetails{}, err
	}
	clients, err := u.repo.ListRejectedClients(ctx, g.ID, domain.RejectedMaxClientsPerGroup)
	if err != nil {
		return RejectedDetails{}, fmt.Errorf("rejected clients: %w", err)
	}
	samples, err := u.repo.ListRejectedSamples(ctx, g.ID, domain.RejectedMaxSamplesPerGroup)
	if err != nil {
		return RejectedDetails{}, fmt.Errorf("rejected samples: %w", err)
	}
	return RejectedDetails{
		Group:      g,
		Clients:    clients,
		Samples:    samples,
		Suggestion: u.suggest(ctx, g),
	}, nil
}

// Resolve помечает группу разобранной.
func (u *RejectedUsecase) Resolve(ctx context.Context, sc RejectedScope, actor Actor, id string) error {
	g, err := u.get(ctx, sc, id)
	if err != nil {
		return err
	}
	if err := u.repo.ResolveRejected(ctx, g.ID, actor.UserLogin, u.clock.Now().UTC()); err != nil {
		return fmt.Errorf("rejected resolve: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionRejectedResolve, "rejected_group", g.ID, map[string]any{
		"team_slug": g.TeamSlug,
		"node_path": g.NodePath,
		"reason":    string(g.Reason),
	})
	return nil
}

// Delete удаляет группу вместе с клиентами и сэмплами.
func (u *RejectedUsecase) Delete(ctx context.Context, sc RejectedScope, actor Actor, id string) error {
	g, err := u.get(ctx, sc, id)
	if err != nil {
		return err
	}
	if err := u.repo.DeleteRejected(ctx, g.ID); err != nil {
		return fmt.Errorf("rejected delete: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionRejectedDelete, "rejected_group", g.ID, map[string]any{
		"team_slug": g.TeamSlug,
		"node_path": g.NodePath,
		"reason":    string(g.Reason),
		"count":     g.Count,
	})
	return nil
}

// get читает группу и проверяет, вправе ли вызывающий её видеть.
//
// Чужая группа отдаётся как «не найдено», а не как 403: по коду ответа иначе
// восстанавливалось бы, какие адреса дёргают в других командах.
func (u *RejectedUsecase) get(ctx context.Context, sc RejectedScope, id string) (*domain.RejectedGroup, error) {
	g, err := u.repo.GetRejected(ctx, id)
	if err != nil {
		return nil, err
	}
	if sc.IsAdmin {
		return g, nil
	}
	slug, err := u.scopeSlug(ctx, sc)
	if err != nil {
		return nil, err
	}
	if slug == "" || g.TeamSlug != slug {
		u.logger.Debug("rejected group is out of caller scope",
			u.logger.Str("group", id),
			u.logger.Str("group_team", g.TeamSlug),
			u.logger.Str("caller_team", slug))
		return nil, domain.ErrRejectedGroupNotFound
	}
	return g, nil
}

// applyScope сужает фильтр до области видимости вызывающего.
func (u *RejectedUsecase) applyScope(ctx context.Context, sc RejectedScope, f port.RejectedFilter) (port.RejectedFilter, error) {
	if sc.IsAdmin {
		f.UnknownTeamOnly = sc.UnknownTeamOnly
		if sc.TeamSlug != "" {
			f.TeamSlugs = []string{sc.TeamSlug}
		}
		return f, nil
	}
	slug, err := u.scopeSlug(ctx, sc)
	if err != nil {
		return f, err
	}
	if slug == "" {
		// Команда сессии не определилась: показывать всё было бы утечкой,
		// поэтому выдача пуста. Пустой НЕ-nil список означает «ни одной
		// команды» — репозиторий трактует его буквально.
		u.logger.Debug("rejected list: caller has no team scope")
		f.TeamSlugs = []string{}
		return f, nil
	}
	f.TeamSlugs = []string{slug}
	f.UnknownTeamOnly = false
	return f, nil
}

// scopeSlug — слог команды сессии.
func (u *RejectedUsecase) scopeSlug(ctx context.Context, sc RejectedScope) (string, error) {
	if sc.TeamID == "" {
		return "", nil
	}
	t, err := u.teams.GetByID(ctx, sc.TeamID)
	if err != nil {
		if errors.Is(err, domain.ErrTeamNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("rejected scope: %w", err)
	}
	return t.Slug, nil
}

// suggestMaxDistance — максимальное расстояние до похожего узла.
const suggestMaxDistance = 3

// suggest ищет существующий узел, на который похож путь из отказа (§94.6).
//
// Работает только для 404: у остальных причин узел существует, и подсказывать
// нечего. Команда берётся по слогу — если такой команды нет, сравнивать не с
// чем (в чужие команды заглядывать нельзя).
func (u *RejectedUsecase) suggest(ctx context.Context, g *domain.RejectedGroup) *RejectedSuggestion {
	if g.Reason != domain.RejectReasonNodeNotFound || g.TeamID == "" || u.nodes == nil {
		return nil
	}
	nodes, err := u.nodes.List(ctx, port.ListNodesFilter{TeamID: g.TeamID})
	if err != nil {
		// Подсказка — украшение: её отсутствие не должно ломать карточку.
		u.logger.Debug("rejected suggestion: node list failed",
			u.logger.Str("team", g.TeamID), u.logger.Err(err))
		return nil
	}
	want := strings.ToLower(g.NodePath)
	best := suggestMaxDistance + 1
	var bestNode *domain.Node
	for _, n := range nodes {
		d := levenshtein(want, strings.ToLower(n.Path))
		if d < best {
			best, bestNode = d, n
		}
	}
	if bestNode == nil || best > suggestMaxDistance || best == 0 {
		// best == 0 означает «узел с таким путём есть»: значит отказ был не про
		// опечатку (узел выключен, другая команда, гонка с созданием), и
		// подсказка «похоже на этот узел» вводила бы в заблуждение.
		return nil
	}
	return &RejectedSuggestion{
		NodeID:   bestNode.ID,
		Path:     bestNode.Path,
		TeamSlug: g.TeamSlug,
		Distance: best,
	}
}

// levenshtein — расстояние редактирования между строками.
//
// Своя реализация вместо зависимости: нужна одна функция на десяток строк,
// вызывается на карточке одной группы по узлам одной команды.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}
