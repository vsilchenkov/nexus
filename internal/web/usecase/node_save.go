package usecase

import (
	"context"
	"maps"

	"nexus/internal/domain"
	"nexus/internal/web/usecase/port"
)

// nodeAudit — какое действие уходит в журнал при записи узла и что к нему
// добавить. Update и SetStatus пишут node.update, отмена из журнала (§108) —
// node.revert с reverted_from/reverted_from_at; конвейер записи общий.
type nodeAudit struct {
	action string
	// op — имя операции для debug-строк кеша и публикации инвалидации.
	op    string
	extra map[string]any
}

// prepareChanged готовит изменённый узел к записи: дефолты, нормализация под
// root_method, полное имя CH-таблицы, валидация, проверки чужой таблицы
// (§70.6) и самоссылки (§32.2). Возвращает имена полей, молча сброшенных
// нормализацией (§27.6), — они уходят в запись аудита.
func (u *NodeUsecase) prepareChanged(ctx context.Context, n *domain.Node) ([]string, error) {
	n.SetDefaults()
	cleared := n.NormalizeForRootMethod() // §27.6
	if err := u.normalizeCHTable(ctx, n); err != nil {
		return nil, err
	}
	if err := n.Validate(); err != nil {
		return nil, err
	}
	// §70.6: имя таблицы не должно вести в БД другой ноды.
	if err := u.assertTableNotForeign(ctx, n); err != nil {
		return nil, err
	}
	// §32.2: target_url не должен указывать на собственный ingress шины.
	if err := u.checkSelfReference(n); err != nil {
		return nil, err
	}
	return cleared, nil
}

// commitChanged записывает подготовленный узел n поверх old: переносит поля,
// которыми форма не управляет, при необходимости (пере)создаёт таблицу логов,
// пишет узел и строку аудита одной транзакцией, затем обновляет кеш и
// сигналит Receiver'у.
func (u *NodeUsecase) commitChanged(ctx context.Context, actor Actor, old, n *domain.Node, cleared []string, a nodeAudit) error {
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
	maps.Copy(diff, a.extra)
	if err := u.writeNodeChange(ctx, actor, n, a.action, diff); err != nil {
		return err
	}
	u.afterNodeChange(ctx, old, n, a.op)
	return nil
}

// commitStatus меняет ТОЛЬКО статус узла (§35): узел сохраняется как есть,
// прочие поля, креды и allowlist-снимок не трогаются, валидации и
// перепровижининга таблицы нет. Лёгкий путь кнопок «Пауза»/«Отключить» и
// отмены из журнала, в которой выбран один статус (§108.4).
func (u *NodeUsecase) commitStatus(ctx context.Context, actor Actor, old *domain.Node, status domain.NodeStatus, a nodeAudit) error {
	updated := *old
	updated.Status = status
	updated.UpdatedBy = actor.UserLogin // §63
	diff := map[string]any{"status": map[string]string{"before": string(old.Status), "after": string(status)}}
	maps.Copy(diff, a.extra)
	if err := u.writeNodeChange(ctx, actor, &updated, a.action, diff); err != nil {
		return err
	}
	u.cacheSet(ctx, &updated, a.op)
	u.publishInvalidate(ctx, updated.TeamID, updated.Path, "")
	return nil
}

// writeNodeChange пишет узел и строку аудита: в одной транзакции, если есть
// UnitOfWork, иначе — двумя шагами (legacy-путь без UoW, аудит best-effort).
func (u *NodeUsecase) writeNodeChange(ctx context.Context, actor Actor, n *domain.Node, action string, details map[string]any) error {
	if u.uow != nil {
		return u.uow.Execute(ctx, func(ctx context.Context, r port.Repos) error {
			if err := r.Nodes.Update(ctx, n); err != nil {
				return err
			}
			return r.Audit.Write(ctx, auditEntry(actor, action, "node", n.ID, details))
		})
	}
	if err := u.repo.Update(ctx, n); err != nil {
		return err
	}
	u.audit.Log(ctx, actor, action, "node", n.ID, details)
	return nil
}

// afterNodeChange — кеш и инвалидация после записи узла. Команда при записи не
// меняется, поэтому старый путь чистится в той же команде.
func (u *NodeUsecase) afterNodeChange(ctx context.Context, old, n *domain.Node, op string) {
	oldPath := ""
	if old.Path != n.Path {
		oldPath = old.Path
		u.cacheInvalidate(ctx, old.TeamID, old.Path, op+" (old path)")
	}
	u.cacheSet(ctx, n, op)
	// §57: гарантированная инвалидация в Receiver. При переименовании передаём и
	// старый путь — иначе старый ключ ещё TTL указывал бы на переехавший узел.
	u.publishInvalidate(ctx, n.TeamID, n.Path, oldPath)
}
