package usecase

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"nexus/internal/domain"
)

// NodeRevert — что вернуть узлу из строки журнала аудита (§108.4).
type NodeRevert struct {
	NodeID string
	// Values — выбранные поля и их значения «Было» из записи журнала (в виде,
	// в котором они пришли из JSONB: float64/bool/string).
	Values map[string]any
	// ExpectedUpdatedAt — updated_at узла, который видел предпросмотр (§108.5).
	ExpectedUpdatedAt time.Time
	// SourceEntryID / SourceEntryAt — отменяемая строка журнала; уходят в
	// details записи node.revert как reverted_from / reverted_from_at.
	SourceEntryID string
	SourceEntryAt time.Time
}

// RevertNotHeldError — нормализация узла сбросила возвращаемые значения
// (§108.4): например, sse_decline у узла с входящим POST или нулевой порт,
// который SetDefaults заменил портом по умолчанию.
type RevertNotHeldError struct {
	Fields []string
}

func (e *RevertNotHeldError) Error() string {
	return fmt.Sprintf("%v: %s", domain.ErrAuditRevertNotHeld, strings.Join(e.Fields, ", "))
}

func (e *RevertNotHeldError) Unwrap() error { return domain.ErrAuditRevertNotHeld }

// Revert возвращает узлу значения полей из строки журнала и пишет строку
// node.revert — в команду УЗЛА, а не текущую команду актора: иначе команда
// узла отмены в своём журнале не увидела бы (§108.6).
//
// Team-скоупа нет: журнал администратора сквозной, а узел мог переехать после
// записи. Вызывающий обязан ограничить доступ ролью admin (§108.1).
//
// Узел изменился после предпросмотра — domain.ErrAuditRevertConflict, узел не
// трогается. Проверка best-effort: между ней и UPDATE окно остаётся (§108.5).
func (u *NodeUsecase) Revert(ctx context.Context, actor Actor, r NodeRevert) error {
	old, err := u.repo.Get(ctx, r.NodeID)
	if err != nil {
		return err
	}
	if !old.UpdatedAt.Equal(r.ExpectedUpdatedAt) {
		u.logger.Debug("node revert: node changed since preview",
			u.logger.Str("node_id", old.ID),
			u.logger.Str("expected_updated_at", r.ExpectedUpdatedAt.UTC().Format(time.RFC3339Nano)),
			u.logger.Str("actual_updated_at", old.UpdatedAt.UTC().Format(time.RFC3339Nano)))
		return domain.ErrAuditRevertConflict
	}
	actor.TeamID = old.TeamID
	a := nodeAudit{action: domain.ActionNodeRevert, op: "revert", extra: map[string]any{
		"reverted_from":    r.SourceEntryID,
		"reverted_from_at": r.SourceEntryAt.UTC().Format(time.RFC3339Nano),
	}}
	if status, ok, err := statusOnlyRevert(r.Values); ok || err != nil {
		if err != nil {
			return err
		}
		if old.Status == status {
			return fmt.Errorf("%w: status", domain.ErrAuditRevertFieldInvalid)
		}
		u.logger.Debug("node revert: status-only path",
			u.logger.Str("node_id", old.ID), u.logger.Str("source_entry", r.SourceEntryID))
		return u.commitStatus(ctx, actor, old, status, a)
	}
	n, cleared, err := u.prepareRevert(ctx, old, r.Values)
	if err != nil {
		return err
	}
	u.logger.Debug("node revert: full save path",
		u.logger.Str("node_id", old.ID), u.logger.Str("source_entry", r.SourceEntryID),
		u.logger.Str("fields", strings.Join(slices.Sorted(maps.Keys(r.Values)), ",")))
	return u.commitChanged(ctx, actor, old, n, cleared, a)
}

// PreviewRevert проверяет отмену без записи (§108.4): тот же выбор пути, что у
// Revert, та же подготовка и проверка «значения удержались». nil — отмену с
// такими полями можно применить. Пустой выбор — nil.
func (u *NodeUsecase) PreviewRevert(ctx context.Context, old *domain.Node, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	if _, ok, err := statusOnlyRevert(values); ok || err != nil {
		return err
	}
	_, _, err := u.prepareRevert(ctx, old, values)
	return err
}

// prepareRevert накладывает значения на КОПИЮ узла, готовит её тем же
// конвейером, что форма узла, и проверяет, что выбранные значения пережили
// нормализацию. Исходный узел не меняется.
func (u *NodeUsecase) prepareRevert(ctx context.Context, old *domain.Node, values map[string]any) (*domain.Node, []string, error) {
	n := cloneNode(old)
	if err := applyAuditValues(n, values); err != nil {
		return nil, nil, err
	}
	cleared, err := u.prepareChanged(ctx, n)
	if err != nil {
		return nil, nil, err
	}
	if lost := notHeldFields(n, values); len(lost) > 0 {
		u.logger.Debug("node revert: values not kept after normalization",
			u.logger.Str("node_id", old.ID), u.logger.Str("fields", strings.Join(lost, ",")))
		return nil, nil, &RevertNotHeldError{Fields: lost}
	}
	return n, cleared, nil
}

// statusOnlyRevert — выбран ровно статус: тогда отмена идёт лёгким путём
// SetStatus, без полной валидации (§108.4). ok=false — путь полный.
func statusOnlyRevert(values map[string]any) (domain.NodeStatus, bool, error) {
	v, ok := values["status"]
	if !ok || len(values) != 1 {
		return "", false, nil
	}
	status, err := decodeAuditValue[domain.NodeStatus](v)
	if err != nil || !status.Valid() {
		return "", true, fmt.Errorf("%w: status", domain.ErrAuditRevertFieldInvalid)
	}
	return status, true, nil
}

// applyAuditValues записывает значения журнала в поля узла по реестру.
// Неизвестное, неприменимое к типу узла или нечитаемое значение —
// domain.ErrAuditRevertFieldInvalid (текст ошибки разбора наружу не уходит).
func applyAuditValues(n *domain.Node, values map[string]any) error {
	for _, name := range slices.Sorted(maps.Keys(values)) {
		f, ok := lookupNodeField(name)
		if !ok || !f.appliesTo(n) {
			return fmt.Errorf("%w: %s", domain.ErrAuditRevertFieldInvalid, name)
		}
		if err := f.set(n, values[name]); err != nil {
			return fmt.Errorf("%w: %s", domain.ErrAuditRevertFieldInvalid, name)
		}
	}
	return nil
}

// notHeldFields — поля, чьё значение после подготовки узла не совпало с
// возвращаемым: нормализация его сбросила или заменила.
func notHeldFields(n *domain.Node, values map[string]any) []string {
	var lost []string
	for _, name := range slices.Sorted(maps.Keys(values)) {
		f, _ := lookupNodeField(name) // имена уже проверены applyAuditValues
		if held, err := f.holds(n, values[name]); err != nil || !held {
			lost = append(lost, name)
		}
	}
	return lost
}

// cloneNode — копия узла без общих изменяемых частей: SetDefaults меняет
// *AsyncAck на месте, а срезы делились бы с исходным узлом.
func cloneNode(n *domain.Node) *domain.Node {
	c := *n
	if n.AsyncAck != nil {
		ack := *n.AsyncAck
		c.AsyncAck = &ack
	}
	c.ForwardHeaders = slices.Clone(n.ForwardHeaders)
	c.URLAllowedHosts = slices.Clone(n.URLAllowedHosts)
	return &c
}
