package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// auditEntryReader — чтение журнала аудита: одна запись по AuditFilter.ID.
type auditEntryReader interface {
	List(ctx context.Context, f port.AuditFilter) ([]*domain.AuditEntry, error)
}

// nodeReverter — применение и проверка отмены (реализует *NodeUsecase).
type nodeReverter interface {
	Revert(ctx context.Context, actor Actor, r NodeRevert) error
	PreviewRevert(ctx context.Context, old *domain.Node, values map[string]any) error
}

// AuditRevertUsecase — отмена изменения узла из журнала аудита и разметка
// неактуальных строк (§108). Доступ — только роль admin; проверка роли на
// стороне handler'а, usecase team-скоупа не применяет (журнал админа сквозной).
type AuditRevertUsecase struct {
	entries  auditEntryReader
	nodes    port.NodeBatchReader
	reverter nodeReverter
	logger   logging.Logger
}

// NewAuditRevertUsecase собирает usecase отмены из журнала.
func NewAuditRevertUsecase(entries auditEntryReader, nodes port.NodeBatchReader, reverter nodeReverter, logger logging.Logger) *AuditRevertUsecase {
	return &AuditRevertUsecase{entries: entries, nodes: nodes, reverter: reverter, logger: logger}
}

// RevertPreview — предпросмотр отмены строки журнала (§108.7).
type RevertPreview struct {
	Entry *domain.AuditEntry
	// Node — текущий узел; nil — узел удалён.
	Node   *domain.Node
	State  RevertState
	Fields []RevertField
	// Moved — строка писалась в другой команде, чем та, где узел сейчас.
	Moved bool
	// DryRunErr — чем закончилась бы отмена выбора по умолчанию: nil —
	// применится (или выбирать по умолчанию нечего).
	DryRunErr error
}

// Preview — строка журнала против текущего узла и пробный прогон отмены
// выбора по умолчанию (поля actual без предупреждения), без записи.
func (u *AuditRevertUsecase) Preview(ctx context.Context, entryID string) (*RevertPreview, error) {
	e, err := u.loadEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	cur, err := u.loadNode(ctx, e.TargetID)
	if err != nil {
		return nil, err
	}
	fields := classifyEntry(e.Details, cur)
	state, _ := revertState(fields, cur)
	p := &RevertPreview{Entry: e, Node: cur, State: state, Fields: fields}
	if cur != nil {
		p.Moved = e.TeamID != "" && e.TeamID != cur.TeamID
		p.DryRunErr = u.reverter.PreviewRevert(ctx, cur, defaultRevertValues(fields))
	}
	u.logger.Debug("audit revert: preview",
		u.logger.Str("entry_id", e.ID), u.logger.Str("state", string(state)),
		slog.Bool("node_found", cur != nil), slog.Bool("dry_run_ok", p.DryRunErr == nil))
	return p, nil
}

// Apply отменяет выбранные поля строки журнала. expected — updated_at узла из
// предпросмотра: узел изменился — domain.ErrAuditRevertConflict (§108.5).
// Поле, которого нет в строке или которое вернуть нельзя, —
// domain.ErrAuditRevertFieldInvalid; узел удалён — domain.ErrNodeNotFound.
func (u *AuditRevertUsecase) Apply(ctx context.Context, actor Actor, entryID string, fields []string, expected time.Time) error {
	e, err := u.loadEntry(ctx, entryID)
	if err != nil {
		return err
	}
	cur, err := u.loadNode(ctx, e.TargetID)
	if err != nil {
		return err
	}
	if cur == nil {
		return domain.ErrNodeNotFound
	}
	// Конфликт проверяется ДО разбора полей: после чужой правки поле могло
	// стать «уже таким», и ответ «поле нельзя отменить» вместо «узел изменился»
	// сбивал бы с толку.
	if !cur.UpdatedAt.Equal(expected) {
		return domain.ErrAuditRevertConflict
	}
	values, err := selectRevertValues(classifyEntry(e.Details, cur), fields)
	if err != nil {
		return err
	}
	return u.reverter.Revert(ctx, actor, NodeRevert{
		NodeID: cur.ID, Values: values, ExpectedUpdatedAt: expected,
		SourceEntryID: e.ID, SourceEntryAt: e.CreatedAt,
	})
}

// Annotate размечает строки node.update/node.revert страницы выдачи журнала:
// состояние, есть ли что отменять, текущие значения перебитых полей. Узлы
// читаются одним запросом на страницу. Ошибка чтения не роняет выдачу —
// возвращается пустая карта (fail-open, §108.7). Строки без разметки
// (не отменяемые, state none) в карту не попадают.
func (u *AuditRevertUsecase) Annotate(ctx context.Context, entries []*domain.AuditEntry) map[string]RevertMark {
	t0 := time.Now()
	ids := revertTargetIDs(entries)
	if len(ids) == 0 {
		return nil
	}
	nodes, err := u.nodes.GetByIDs(ctx, ids)
	if err != nil {
		u.logger.Warn("audit revert: annotate nodes lookup failed, list served without marks",
			u.logger.Int("nodes", len(ids)), u.logger.Err(err))
		return nil
	}
	marks := make(map[string]RevertMark, len(entries))
	for _, e := range entries {
		if !isRevertibleEntry(e) {
			continue
		}
		cur := nodes[e.TargetID]
		if m := markOf(classifyEntry(e.Details, cur), cur); m.State != RevertStateNone {
			marks[e.ID] = m
		}
	}
	u.logger.Debug("audit revert: annotated page",
		u.logger.Int("entries", len(entries)), u.logger.Int("nodes_requested", len(ids)),
		u.logger.Int("nodes_found", len(nodes)), u.logger.Int("marks", len(marks)),
		u.logger.Int("duration_ms", int(time.Since(t0).Milliseconds())))
	return marks
}

// loadEntry — отменяемая строка журнала по id. Не UUID — «не найдено» до
// запроса (::uuid в SQL дал бы 22P02 и 500). Запись берётся с совпадением id:
// читатель, проигнорировавший фильтр, вернул бы чужую строку — и отменилась
// бы она.
func (u *AuditRevertUsecase) loadEntry(ctx context.Context, entryID string) (*domain.AuditEntry, error) {
	id, err := uuid.Parse(entryID)
	if err != nil {
		return nil, domain.ErrAuditEntryNotFound
	}
	list, err := u.entries.List(ctx, port.AuditFilter{ID: id.String(), Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("load audit entry: %w", err)
	}
	for _, e := range list {
		if e.ID != id.String() {
			continue
		}
		if !isRevertibleEntry(e) {
			return nil, domain.ErrAuditNotRevertible
		}
		return e, nil
	}
	return nil, domain.ErrAuditEntryNotFound
}

// loadNode — текущий узел строки; nil без ошибки — узел удалён.
func (u *AuditRevertUsecase) loadNode(ctx context.Context, nodeID string) (*domain.Node, error) {
	if !isUUID(nodeID) {
		return nil, nil // невалидный id узла — такого узла нет
	}
	nodes, err := u.nodes.GetByIDs(ctx, []string{nodeID})
	if err != nil {
		return nil, fmt.Errorf("load node for audit revert: %w", err)
	}
	return nodes[nodeID], nil
}

// isRevertibleEntry — строка, которую §108 умеет отменять.
func isRevertibleEntry(e *domain.AuditEntry) bool {
	return e.TargetType == "node" &&
		(e.Action == domain.ActionNodeUpdate || e.Action == domain.ActionNodeRevert)
}

// revertTargetIDs — уникальные валидные id узлов отменяемых строк.
func revertTargetIDs(entries []*domain.AuditEntry) []string {
	seen := map[string]bool{}
	var ids []string
	for _, e := range entries {
		if !isRevertibleEntry(e) || seen[e.TargetID] {
			continue
		}
		seen[e.TargetID] = true
		if isUUID(e.TargetID) {
			ids = append(ids, e.TargetID)
		}
	}
	return ids
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// defaultRevertValues — выбор по умолчанию: поля, совпадающие со «Стало»,
// без предупреждения о кредах (§108.3).
func defaultRevertValues(fields []RevertField) map[string]any {
	values := map[string]any{}
	for _, f := range fields {
		if f.Status == RevertFieldActual && f.Warning == "" {
			values[f.Name] = f.Before
		}
	}
	return values
}

// selectRevertValues — значения «Было» выбранных полей. Пустой выбор, дубль,
// поле, которого нет в строке или которое вернуть нельзя, —
// domain.ErrAuditRevertFieldInvalid.
func selectRevertValues(fields []RevertField, names []string) (map[string]any, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: no fields selected", domain.ErrAuditRevertFieldInvalid)
	}
	byName := make(map[string]RevertField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	values := make(map[string]any, len(names))
	for _, name := range names {
		f, ok := byName[name]
		_, dup := values[name]
		if !ok || dup || !f.Revertible() {
			return nil, fmt.Errorf("%w: %s", domain.ErrAuditRevertFieldInvalid, name)
		}
		values[name] = f.Before
	}
	return values, nil
}
