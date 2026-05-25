// Package usecase — бизнес-логика Web Service (§17.2 ТЗ).
package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

// NodeUsecase — CRUD над узлами + write-through кеш + audit-log.
type NodeUsecase struct {
	repo            port.NodeRepo
	cache           port.NodeCache
	audit           *AuditUsecase
	cacheTTL        time.Duration
	nodesHardLimit  int
	logger          logging.Logger
}

func NewNodeUsecase(
	repo port.NodeRepo,
	cache port.NodeCache,
	audit *AuditUsecase,
	cacheTTL time.Duration,
	nodesHardLimit int,
	logger logging.Logger,
) *NodeUsecase {
	return &NodeUsecase{
		repo:           repo,
		cache:          cache,
		audit:          audit,
		cacheTTL:       cacheTTL,
		nodesHardLimit: nodesHardLimit,
		logger:         logger,
	}
}

func (u *NodeUsecase) Get(ctx context.Context, id string) (*domain.Node, error) {
	n, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get node: %w", err)
	}
	return n, nil
}

func (u *NodeUsecase) List(ctx context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	if f.TeamID == "" {
		f.TeamID = "default"
	}
	return u.repo.List(ctx, f)
}

func (u *NodeUsecase) Create(ctx context.Context, actor Actor, n *domain.Node) error {
	n.SetDefaults()
	if err := n.Validate(); err != nil {
		return err
	}

	// §3.3 ТЗ: hard-limit nodes_hard_limit.
	count, err := u.repo.Count(ctx, n.TeamID)
	if err != nil {
		return fmt.Errorf("count nodes for limit check: %w", err)
	}
	if u.nodesHardLimit > 0 && count >= u.nodesHardLimit {
		return domain.ErrLimitReached
	}

	if err := u.repo.Create(ctx, n); err != nil {
		return err
	}
	// Write-through: после создания кладём в кеш.
	if err := u.cache.Set(ctx, n, u.cacheTTL); err != nil {
		u.logger.Warn("cache set after create failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
	u.audit.Log(ctx, actor, domain.ActionNodeCreate, "node", n.ID, map[string]any{
		"path":         n.Path,
		"root_method":  string(n.RootMethod),
		"target_url":   n.TargetURL,
		"url_mode":     string(n.URLMode),
		"auth_type":    string(n.AuthType),
		"status":       string(n.Status),
	})
	return nil
}

func (u *NodeUsecase) Update(ctx context.Context, actor Actor, n *domain.Node) error {
	n.SetDefaults()
	if err := n.Validate(); err != nil {
		return err
	}
	// Старая запись нужна, чтобы инвалидировать кеш по старому path,
	// если path был изменён.
	old, err := u.repo.Get(ctx, n.ID)
	if err != nil {
		return err
	}
	if err := u.repo.Update(ctx, n); err != nil {
		return err
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
	u.audit.Log(ctx, actor, domain.ActionNodeUpdate, "node", n.ID,
		diffNodes(old, n))
	return nil
}

func (u *NodeUsecase) Delete(ctx context.Context, actor Actor, id string) error {
	n, err := u.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNodeNotFound) {
			return err
		}
		return fmt.Errorf("get before delete: %w", err)
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	if err := u.cache.InvalidateByPath(ctx, n.Path); err != nil {
		u.logger.Warn("cache invalidate after delete failed",
			u.logger.Str("path", n.Path), u.logger.Err(err))
	}
	u.audit.Log(ctx, actor, domain.ActionNodeDelete, "node", n.ID, map[string]any{
		"path":             n.Path,
		"clickhouse_table": n.ClickHouseTable,
	})
	return nil
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
	add("status", string(old.Status), string(n.Status))
	add("timeout_ms", old.TimeoutMs, n.TimeoutMs)
	add("retry_count", old.RetryCount, n.RetryCount)
	if old.AuthCredentials != n.AuthCredentials {
		d["auth_credentials"] = "changed"
	}
	if old.IncomingAuthCredentials != n.IncomingAuthCredentials {
		d["incoming_auth_credentials"] = "changed"
	}
	return d
}
