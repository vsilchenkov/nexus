package port

import (
	"context"

	"nexus/internal/domain"
	"nexus/internal/domain/logsearch"
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

	// BeforeID — тай-брейкер keyset-пагинации (§44/45-fix): ID самой старой строки
	// предыдущей страницы. Вместе с UntilMs даёт строгий составной курсор
	// (date_request, ID), чтобы пагинация перешагивала «плотные» секунды (сотни
	// строк с одинаковым date_request секундной точности) — иначе при `<=` по
	// одной секунде следующая страница возвращала те же строки → дедуп → стопор
	// скролла. Пусто → первая страница / без keyset (прежнее `<= UntilMs`).
	BeforeID string

	Limit int

	// IP / Host — exact match.
	IP   string
	Host string

	// Status — "ok" (200..299), "err" (>=400 или 0), "" (любой).
	Status string

	// Done — "yes" / "no" / "" (любой).
	Done string

	// Method — exact match по колонке method (§39 подпуть запроса; §48).
	Method string

	// Q — сырой ввод поля «Поиск» (мини-язык §48.1 либо RE2 в regex-режиме).
	// Адаптер это поле НЕ использует — usecase парсит его в QExpr.
	Q string

	// QCase / QWord / QRegex — режимы поиска (§48.2, кнопки Aa / ab| / .*).
	// Сырые флаги из query-параметров; влияют на разбор Q в usecase.
	QCase  bool
	QWord  bool
	QRegex bool

	// QExpr — распарсенный Q (заполняет usecase через logsearch.Parse).
	// Единственный источник поискового условия для адаптера (SQL) и
	// in-memory зеркала live-tail. nil → полнотекстовый фильтр выключен.
	QExpr *logsearch.Expr
}

// LogReader — read-only доступ к ClickHouse-логам узлов (§7.4 ТЗ).
// Используется replay (§7.4.1) и live-tail (§7.4).
type LogReader interface {
	// GetByID — найти одну запись в указанной таблице (с ПОЛНЫМИ телами).
	// Используется replay (§7.4.1), которому нужно исходное тело целиком.
	GetByID(ctx context.Context, table, id string) (*domain.LogRecord, error)

	// GetByIDPreview — метаданные записи + ОГРАНИЧЕННОЕ превью тел (первые
	// previewRunes рун request/response) + их полные длины в рунах (§42).
	// Один запрос к ClickHouse, не тянет тела целиком — для дефолтного
	// разворачивания строки лога в UI, чтобы большой ответ не вешал фронт.
	// previewRunes <= 0 → дефолт адаптера. Запись не найдена → domain.ErrNotFound.
	GetByIDPreview(ctx context.Context, table, id string, previewRunes int) (rec *domain.LogRecord, reqRunes, respRunes int64, err error)

	// GetBodyChunk — срез одного тела (which = "request"|"response") записи по
	// рунам: substringUTF8(col, offsetRunes+1, limitRunes) + полная длина (§42).
	// Для постраничной подгрузки «показать весь» и потокового скачивания —
	// ships только запрошенный срез, не всё тело. which вне whitelist → ошибка.
	GetBodyChunk(ctx context.Context, table, id, which string, offsetRunes, limitRunes int) (chunk string, totalRunes int64, err error)

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

	// DistinctMethods — уникальные непустые значения колонки method узла
	// (§48.3, фасет дропдауна Method), отсортированные, до limit (кап адаптера).
	// §37: nodeID фильтрует per-node на общей таблице.
	DistinctMethods(ctx context.Context, table, nodeID string, limit int) ([]string, error)

	// DateRange — min/max date_request узла в UnixMilli (§48.3, фасет
	// ограничения полей дат). Пустая таблица/нет записей узла → (0, 0).
	DateRange(ctx context.Context, table, nodeID string) (minMs, maxMs int64, err error)
}
