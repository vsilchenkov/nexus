package usecase

import (
	"context"
	"fmt"
	"slices"
	"time"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// Copy создаёт клон узла sourceID с новым path в той же команде (§53).
//
// Клонируются все настройки, включая креды: repo.Get возвращает их
// расшифрованными (plaintext в памяти — carved-in правило), pg-адаптер
// перешифрует при INSERT; через API значения не проходят. Копия всегда
// создаётся в статусе paused — копия активного узла не должна молча начать
// принимать трафик (для pull-узла это же исключает мгновенную конкуренцию
// двух узлов за одну RMQ-очередь).
//
// ClickHouseTable копируется как есть: несколько узлов могут писать в одну
// таблицу (§37 — атрибуция по node_id), provisionTable идемпотентен
// (CREATE TABLE IF NOT EXISTS).
//
// Ссылки allowlist-хостов (§23) клонируются в той же UoW-транзакции, снимок
// url_allowed_hosts пересобирается для нового узла — иначе копия узла с
// url_mode=from_request получила бы пустой allowlist (fail-closed) и
// блокировала все запросы.
//
// teamID — scope multi-tenancy: узел чужой команды → ErrNodeNotFound.
func (u *NodeUsecase) Copy(ctx context.Context, actor Actor, sourceID, newPath, teamID string) (*domain.Node, error) {
	src, err := u.repo.Get(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if teamID != "" && src.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}

	clone := cloneNodeForCopy(src, newPath)
	// §63: автор копии — тот, кто копирует (не автор источника).
	clone.CreatedBy = actor.UserLogin
	if _, err := u.prepareNewNode(ctx, clone); err != nil {
		return nil, err
	}

	auditDetails := map[string]any{
		"source_node_id": src.ID,
		"source_path":    src.Path,
		"path":           clone.Path,
		"status":         string(clone.Status),
	}

	if u.uow != nil {
		if err := u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Create(ctx, clone); err != nil {
				return err
			}
			cloned, err := u.copyHostLinks(ctx, r, src.ID, clone)
			if err != nil {
				return err
			}
			auditDetails["allowed_hosts_cloned"] = cloned
			return r.Audit.Write(ctx, auditEntry(actor, domain.ActionNodeCopy, "node", clone.ID, auditDetails))
		}); err != nil {
			return nil, err
		}
	} else {
		if err := u.repo.Create(ctx, clone); err != nil {
			return nil, err
		}
		u.audit.Log(ctx, actor, domain.ActionNodeCopy, "node", clone.ID, auditDetails)
		// §51.9: без UoW ссылки хостов не клонируем — Link + пересборка снимка
		// вне транзакции могли бы разъехаться с созданием узла (CLI/тесты).
		u.logger.Debug("node copy: uow is nil, host allowlist links not cloned",
			u.logger.Str("source_id", src.ID), u.logger.Str("path", clone.Path))
	}

	u.cacheSet(ctx, clone, "copy")
	return clone, nil
}

// cloneNodeForCopy — копия узла под вставку как нового (§53): сбрасывает
// ID/таймстемпы, задаёт новый path и принудительно paused; слайсы копируются,
// чтобы не алиасить память источника. Креды переносятся как есть.
func cloneNodeForCopy(src *domain.Node, newPath string) *domain.Node {
	clone := *src
	clone.ID = ""
	clone.Path = newPath
	clone.Status = domain.NodeStatusPaused
	clone.CreatedAt = time.Time{}
	clone.UpdatedAt = time.Time{}
	// §63: автор проставляется заново в Copy (клон копирует поля источника).
	clone.CreatedBy = ""
	clone.UpdatedBy = ""
	clone.ForwardHeaders = slices.Clone(src.ForwardHeaders)
	// Снимок allowlist сбрасывает prepareNewNode; пересборка — copyHostLinks.
	clone.URLAllowedHosts = nil
	return &clone
}

// copyHostLinks клонирует привязки allowlist-хостов источника на новый узел и
// пересобирает его снимок url_allowed_hosts (§23). Возвращает число ссылок.
func (u *NodeUsecase) copyHostLinks(ctx context.Context, r port.Repos, sourceID string, clone *domain.Node) (int, error) {
	if r.Hosts == nil {
		// §51.9: конфигурация без host-allowlist репо — тихий пропуск виден на debug.
		u.logger.Debug("node copy: hosts repo unavailable, links not cloned",
			u.logger.Str("source_id", sourceID))
		return 0, nil
	}
	entries, err := r.Hosts.ListByNode(ctx, sourceID)
	if err != nil {
		return 0, fmt.Errorf("list source host links: %w", err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	for _, e := range entries {
		if err := r.Hosts.Link(ctx, clone.ID, e.ID); err != nil {
			return 0, fmt.Errorf("link host %s: %w", e.ID, err)
		}
	}
	patterns := encodeHostPatterns(entries)
	// §63: снимок бампает updated_at; в одной UoW-транзакции now() совпадает с
	// INSERT'ом, поэтому created_at==updated_at сохранится (у копии «Обновлено»
	// скрыто). updatedBy = автор копии для консистентности.
	if err := r.Nodes.UpdateAllowedHostsSnapshot(ctx, clone.ID, patterns, clone.CreatedBy); err != nil {
		return 0, fmt.Errorf("update allowed hosts snapshot: %w", err)
	}
	clone.URLAllowedHosts = patterns
	return len(entries), nil
}
