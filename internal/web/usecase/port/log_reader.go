package port

import (
	"context"

	"nexus/internal/domain"
)

// LogQuery — параметры расширенного поиска по логам узла (§7.4, Phase 6.8).
// Все поля опциональны, нулевые значения = «не фильтровать».
type LogQuery struct {
	Table string

	// NodeID — §37: UUID узла для per-node атрибуции в общей CH-таблице.
	// Пусто → без фильтра по узлу (legacy/одна-таблица-один-узел).
	NodeID string

	// SinceMs / UntilMs — диапазон по date_request (UnixMilli).
	// SinceMs > 0 — включается WHERE > since (строгий).
	// UntilMs > 0 — WHERE <= until (включительный).
	SinceMs int64
	UntilMs int64

	Limit int

	// IP / Host — exact match.
	IP   string
	Host string

	// Status — "ok" (200..299), "err" (>=400 или 0), "" (любой).
	Status string

	// Done — "yes" / "no" / "" (любой).
	Done string

	// Q — подстрока полнотекстового поиска по URL + Request + Response.
	// Регистронезависимый поиск через positionCaseInsensitiveUTF8.
	Q string
}

// LogReader — read-only доступ к ClickHouse-логам узлов (§7.4 ТЗ).
// Используется replay (§7.4.1) и live-tail (§7.4).
type LogReader interface {
	// GetByID — найти одну запись в указанной таблице.
	GetByID(ctx context.Context, table, id string) (*domain.LogRecord, error)

	// ListSince — записи узла nodeID после cursor (date_request > cursor) по
	// таблице, ASC. Используется для SSE live-tail. §37: nodeID фильтрует
	// per-node на общей таблице (пусто — без фильтра).
	ListSince(ctx context.Context, table, nodeID string, cursor int64, limit int) ([]*domain.LogRecord, error)

	// Search — snapshot с расширенными фильтрами (Phase 6.8). Сортировка
	// по date_request DESC (последние записи первыми), LIMIT.
	Search(ctx context.Context, q LogQuery) ([]*domain.LogRecord, error)

	// CountErrors — число записей-ошибок (status>=400 OR status=0 OR done=0)
	// в таблице за окно (sinceMs, untilMs]. Используется уведомлениями (§20.3).
	CountErrors(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error)

	// CountFailed — число НЕдоставленных записей (строго done=0) в таблице за
	// окно (sinceMs, untilMs]. §35: KPI «неудачные доставки» на вкладке «Очередь»
	// (каждая такая запись соответствует сообщению, ушедшему в DLQ). Уже,
	// чем CountErrors (тот включает status>=400 даже при done=1 — невозможно
	// для async, но семантически отдельный сигнал).
	CountFailed(ctx context.Context, table, nodeID string, sinceMs, untilMs int64) (uint64, error)

	// FailedIDs — уникальные ID записей done=0 за окно (sinceMs, untilMs], до cap
	// (capped=true, если есть ещё). §36: используется «Очистить все неудачные»
	// (qcancel) и «Повторить все сейчас» (массовый replay) — общий набор сообщений.
	// §37: nodeID фильтрует per-node на общей таблице.
	FailedIDs(ctx context.Context, table, nodeID string, sinceMs, untilMs int64, cap int) ([]string, bool, error)
}
