package usecase

import (
	"context"
	"errors"
	"net/url"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// HostAllowlistUsecase — управление каталогом разрешённых хостов (§23) и
// привязкой паттернов к узлам.
//
// Источник истины для allowlist узла — таблица node_allowed_hosts. При
// привязке/отвязке пересобирается денормализованный снимок
// nodes.url_allowed_hosts (его читает Receiver из кеша) и обновляется
// write-through кеш узла. Link/unlink + пересборка снимка + audit идут одной
// UoW-транзакцией; при uow=nil — последовательный fallback (unit-тесты).
type HostAllowlistUsecase struct {
	repo     port.HostAllowlistRepo
	nodeRepo port.NodeRepo
	cache    port.NodeCache
	uow      port.UnitOfWork
	audit    *AuditUsecase
	cacheTTL time.Duration
	logger   logging.Logger
}

func NewHostAllowlistUsecase(
	repo port.HostAllowlistRepo,
	nodeRepo port.NodeRepo,
	cache port.NodeCache,
	uow port.UnitOfWork,
	audit *AuditUsecase,
	cacheTTL time.Duration,
	logger logging.Logger,
) *HostAllowlistUsecase {
	return &HostAllowlistUsecase{
		repo:     repo,
		nodeRepo: nodeRepo,
		cache:    cache,
		uow:      uow,
		audit:    audit,
		cacheTTL: cacheTTL,
		logger:   logger,
	}
}

// Search возвращает страницу каталога с фильтром по типу и подстроке.
func (u *HostAllowlistUsecase) Search(ctx context.Context, q string, kind domain.HostKind, limit int) ([]*domain.HostAllowlistEntry, error) {
	return u.repo.Search(ctx, q, kind, limit)
}

func (u *HostAllowlistUsecase) Get(ctx context.Context, id string) (*domain.HostAllowlistEntry, error) {
	return u.repo.Get(ctx, id)
}

// Create добавляет паттерн в каталог. Идемпотентно по case-insensitive
// паттерну: повторный вызов с тем же паттерном возвращает существующую запись
// (для inline-создания из combobox формы узла, §23).
func (u *HostAllowlistUsecase) Create(ctx context.Context, actor Actor, e *domain.HostAllowlistEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	err := u.repo.Create(ctx, e)
	if errors.Is(err, domain.ErrHostAlreadyExists) {
		existing, getErr := u.repo.GetByPattern(ctx, e.Pattern)
		if getErr != nil {
			return getErr
		}
		*e = *existing
		return nil
	}
	if err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionHostCreate, "host", e.ID, map[string]any{
		"pattern": e.Pattern,
		"kind":    string(e.Kind),
	})
	return nil
}

// Update меняет описание (всегда) и паттерн/тип (только если usage_count = 0 —
// иначе денормализованные снимки узлов разъедутся).
func (u *HostAllowlistUsecase) Update(ctx context.Context, actor Actor, id, pattern string, kind domain.HostKind, description string) error {
	cand := &domain.HostAllowlistEntry{Pattern: pattern, Kind: kind, Description: description}
	if err := cand.Validate(); err != nil {
		return err
	}
	existing, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	patternChanged := existing.Pattern != pattern || existing.Kind != kind
	if patternChanged {
		if existing.UsageCount > 0 {
			return domain.ErrHostInUse
		}
		if err := u.repo.UpdatePattern(ctx, id, pattern, kind); err != nil {
			return err
		}
	}
	if existing.Description != description {
		if err := u.repo.UpdateDescription(ctx, id, description); err != nil {
			return err
		}
	}
	u.audit.Log(ctx, actor, domain.ActionHostUpdate, "host", id, map[string]any{
		"pattern":         pattern,
		"kind":            string(kind),
		"pattern_changed": patternChanged,
	})
	return nil
}

// Delete удаляет паттерн. Запрещено, если он используется хотя бы одним узлом
// (usage_count > 0); FK RESTRICT — второй уровень защиты.
func (u *HostAllowlistUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	e, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.UsageCount > 0 {
		return domain.ErrHostInUse
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, domain.ActionHostDelete, "host", id, map[string]any{
		"pattern": e.Pattern,
	})
	return nil
}

// PreviewResult — классификация тестовых URL для диалога создания паттерна.
type PreviewResult struct {
	Allowed []string
	Blocked []string
}

// Preview прогоняет тестовые URL через тот же матчер, что Receiver
// (domain.HostAllowed), чтобы UI показал «Разрешит / Заблокирует» без
// дублирования логики на клиенте (§23). Невалидный паттерн → ошибка.
func (u *HostAllowlistUsecase) Preview(ctx context.Context, pattern string, kind domain.HostKind, testURLs []string) (*PreviewResult, error) {
	_ = ctx
	e := &domain.HostAllowlistEntry{Pattern: pattern, Kind: kind}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	encoded := []string{e.EncodedPattern()}
	res := &PreviewResult{Allowed: []string{}, Blocked: []string{}}
	for _, raw := range testURLs {
		parsed, perr := url.Parse(raw)
		if perr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			res.Blocked = append(res.Blocked, raw)
			continue
		}
		if domain.HostAllowed(parsed.Host, encoded) {
			res.Allowed = append(res.Allowed, raw)
		} else {
			res.Blocked = append(res.Blocked, raw)
		}
	}
	return res, nil
}

// ListByNode возвращает паттерны, привязанные к узлу (chips формы узла).
// teamID — scope multi-tenancy: чужой узел → ErrNodeNotFound.
func (u *HostAllowlistUsecase) ListByNode(ctx context.Context, nodeID, teamID string) ([]*domain.HostAllowlistEntry, error) {
	if err := u.checkNodeScope(ctx, nodeID, teamID); err != nil {
		return nil, err
	}
	return u.repo.ListByNode(ctx, nodeID)
}

// Attach привязывает паттерн к узлу, пересобирает снимок и обновляет кеш.
func (u *HostAllowlistUsecase) Attach(ctx context.Context, actor Actor, nodeID, hostID, teamID string) error {
	if err := u.checkNodeScope(ctx, nodeID, teamID); err != nil {
		return err
	}
	host, err := u.repo.Get(ctx, hostID)
	if err != nil {
		return err
	}
	details := map[string]any{"node_id": nodeID, "host_pattern": host.Pattern}
	if err := u.mutateLink(ctx, actor, nodeID, hostID, true, details); err != nil {
		return err
	}
	u.refreshCache(ctx, nodeID)
	return nil
}

// Detach отвязывает паттерн от узла, пересобирает снимок и обновляет кеш.
func (u *HostAllowlistUsecase) Detach(ctx context.Context, actor Actor, nodeID, hostID, teamID string) error {
	if err := u.checkNodeScope(ctx, nodeID, teamID); err != nil {
		return err
	}
	host, err := u.repo.Get(ctx, hostID)
	if err != nil {
		return err
	}
	details := map[string]any{"node_id": nodeID, "host_pattern": host.Pattern}
	if err := u.mutateLink(ctx, actor, nodeID, hostID, false, details); err != nil {
		return err
	}
	u.refreshCache(ctx, nodeID)
	return nil
}

// mutateLink выполняет link/unlink + пересборку снимка + audit одной
// транзакцией (или последовательно при uow=nil).
func (u *HostAllowlistUsecase) mutateLink(ctx context.Context, actor Actor, nodeID, hostID string, attach bool, details map[string]any) error {
	action := domain.ActionHostDetach
	if attach {
		action = domain.ActionHostAttach
	}
	apply := func(hosts port.HostAllowlistRepo, nodes port.NodeRepo) error {
		var err error
		if attach {
			err = hosts.Link(ctx, nodeID, hostID)
		} else {
			err = hosts.Unlink(ctx, nodeID, hostID)
		}
		if err != nil {
			return err
		}
		entries, err := hosts.ListByNode(ctx, nodeID)
		if err != nil {
			return err
		}
		return nodes.UpdateAllowedHostsSnapshot(ctx, nodeID, encodeHostPatterns(entries))
	}

	if u.uow != nil {
		return u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := apply(r.Hosts, r.Nodes); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, action, "node", nodeID, details))
		})
	}
	if err := apply(u.repo, u.nodeRepo); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, action, "node", nodeID, details)
	return nil
}

// refreshCache перечитывает узел и обновляет write-through кеш свежим снимком.
// Best-effort: ошибка кеша логируется, но не валит операцию (PG авторитетен).
func (u *HostAllowlistUsecase) refreshCache(ctx context.Context, nodeID string) {
	n, err := u.nodeRepo.Get(ctx, nodeID)
	if err != nil {
		u.logger.Warn("host allowlist: reload node for cache failed",
			u.logger.Str("node_id", nodeID), u.logger.Err(err))
		return
	}
	if err := u.cache.Set(ctx, n, u.cacheTTL); err != nil {
		u.logger.Warn("host allowlist: cache set failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
}

func (u *HostAllowlistUsecase) checkNodeScope(ctx context.Context, nodeID, teamID string) error {
	n, err := u.nodeRepo.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if teamID != "" && n.TeamID != teamID {
		return domain.ErrNodeNotFound
	}
	return nil
}

// encodeHostPatterns собирает денормализованный снимок из записей каталога
// (regex → re:<pattern>).
func encodeHostPatterns(entries []*domain.HostAllowlistEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.EncodedPattern())
	}
	return out
}
