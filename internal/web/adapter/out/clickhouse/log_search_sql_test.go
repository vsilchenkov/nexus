package clickhouse

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/logsearch"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TestExprConds — генерация WHERE из AST мини-языка (§48): И/ИЛИ/НЕ,
// скоуп на конкретное поле, режимы Aa/ab|/.*, порядок позиционных args.
func TestExprConds(t *testing.T) {
	t.Parallel()

	// Сокращения ожидаемых условий.
	const (
		ciURL  = "positionCaseInsensitiveUTF8(url, ?) > 0"
		ciPar  = "positionCaseInsensitiveUTF8(parameters, ?) > 0"
		ciReq  = "positionCaseInsensitiveUTF8(request, ?) > 0"
		ciResp = "positionCaseInsensitiveUTF8(response, ?) > 0"
		all4ci = "(" + ciURL + " OR " + ciPar + " OR " + ciReq + " OR " + ciResp + ")"
	)

	tests := []struct {
		name     string
		q        string
		opts     logsearch.Options
		wantCond string
		wantArgs []any
	}{
		{
			name:     "один plain-терм — 4 колонки, регистронезависимо",
			q:        "foo",
			wantCond: "(" + all4ci + ")",
			wantArgs: []any{"foo", "foo", "foo", "foo"},
		},
		{
			name:     "Aa: регистрозависимый position",
			q:        "url:Foo",
			opts:     logsearch.Options{CaseSensitive: true},
			wantCond: "((positionUTF8(url, ?) > 0))",
			wantArgs: []any{"Foo"},
		},
		{
			name:     "скоуп на конкретное поле — одна колонка",
			q:        "params:id=42",
			wantCond: "((" + ciPar + "))",
			wantArgs: []any{"id=42"},
		},
		{
			name:     "И: два терма в одной группе",
			q:        "a & url:b",
			wantCond: "(" + all4ci + " AND (" + ciURL + "))",
			wantArgs: []any{"a", "a", "a", "a", "b"},
		},
		{
			name:     "ИЛИ: две группы",
			q:        "url:a | resp:b",
			wantCond: "(((" + ciURL + ")) OR ((" + ciResp + ")))",
			wantArgs: []any{"a", "b"},
		},
		{
			name:     "НЕ: негация терма",
			q:        "-req:x",
			wantCond: "(NOT (" + ciReq + "))",
			wantArgs: []any{"x"},
		},
		{
			name:     "НЕ по всем колонкам",
			q:        "-x",
			wantCond: "(NOT " + all4ci + ")",
			wantArgs: []any{"x", "x", "x", "x"},
		},
		{
			name: "комбинация: (a И НЕ b) ИЛИ (url:c)",
			q:    "a & -b | url:c",
			wantCond: "((" + all4ci + " AND NOT " + all4ci + ") OR " +
				"((" + ciURL + ")))",
			wantArgs: []any{"a", "a", "a", "a", "b", "b", "b", "b", "c"},
		},
		{
			name:     "ab|: целое слово → match() с готовым паттерном",
			q:        "url:cat",
			opts:     logsearch.Options{WholeWord: true},
			wantCond: "((match(url, ?)))",
			wantArgs: []any{`(?i)\b(?:cat)\b`},
		},
		{
			name: ".*: regex — одно выражение по 4 колонкам",
			q:    "sta(tus|te)",
			opts: logsearch.Options{Regex: true},
			wantCond: "((match(url, ?) OR match(parameters, ?) OR " +
				"match(request, ?) OR match(response, ?)))",
			wantArgs: []any{"(?i)sta(tus|te)", "(?i)sta(tus|te)", "(?i)sta(tus|te)", "(?i)sta(tus|te)"},
		},
		{
			name:     ".* + Aa + ab|: паттерн без (?i), с \\b",
			q:        "cat",
			opts:     logsearch.Options{Regex: true, CaseSensitive: true, WholeWord: true},
			wantCond: "((match(url, ?) OR match(parameters, ?) OR match(request, ?) OR match(response, ?)))",
			wantArgs: []any{`\b(?:cat)\b`, `\b(?:cat)\b`, `\b(?:cat)\b`, `\b(?:cat)\b`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e, err := logsearch.Parse(tt.q, tt.opts)
			require.NoError(t, err)
			cond, args := exprConds(e)
			assert.Equal(t, tt.wantCond, cond)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestSearchConds_StatusDone — SQL быстрых фильтров «ОК»/«Ошибки»/«Завершено»/
// «В работе» (§72.1). До §72.1 это покрывалось только E2E-тестами, чьи фикстуры
// не содержали ни 3xx, ни «2xx + done=0», — то есть прежняя (неполная)
// семантика вообще ничем не была запинена.
//
// Главное, что проверяется: «Ошибки» — ПОЛНОЕ ДОПОЛНЕНИЕ «ОК». Иначе запись с
// 3xx не попадает ни в один фильтр и исчезает из UI.
func TestSearchConds_StatusDone(t *testing.T) {
	t.Parallel()

	const okCond = "(done = 1 AND status >= 200 AND status < 400)"

	tests := []struct {
		name      string
		q         port.LogQuery
		wantConds []string
	}{
		{name: "без фильтров — условий нет", q: port.LogQuery{}},
		{name: "ok = доставлено (done=1 и 2xx/3xx)", q: port.LogQuery{Status: "ok"}, wantConds: []string{okCond}},
		{name: "err = полное дополнение ok", q: port.LogQuery{Status: "err"}, wantConds: []string{"NOT " + okCond}},
		{name: "неизвестный status игнорируется", q: port.LogQuery{Status: "bogus"}},
		{name: "done=yes", q: port.LogQuery{Done: "yes"}, wantConds: []string{"done = 1"}},
		{name: "done=no", q: port.LogQuery{Done: "no"}, wantConds: []string{"done = 0"}},
		{
			name:      "status и done комбинируются",
			q:         port.LogQuery{Status: "err", Done: "no"},
			wantConds: []string{"NOT " + okCond, "done = 0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// NodeID пуст → nodeFilter не обращается ни к CH, ни к PostgreSQL:
			// строится чистый SQL без единого запроса.
			r := NewLogReader(nil, logging.NewNoop())
			conds, args := r.searchConds(context.Background(), tt.q)
			assert.Equal(t, tt.wantConds, conds)
			assert.Empty(t, args, "status/done — константные предикаты, без позиционных аргументов")
		})
	}
}

// TestSearchConds_TimeWindow — окно по date_request, keyset-курсор (§44/45-fix)
// и его дубль по колонке PARTITION BY (§72.4).
//
// Проверяются две вещи. Первая: при DateCreateAligned окно дублируется по
// date_create с запасом ±1 день — без этого ClickHouse читает таблицу целиком
// (замер: 200 000 строк против 12 672). Вторая, более важная: без флага
// (внешняя таблица §64) условий по date_create нет вообще — там инвариант
// «date_create == UTC-день date_request» не гарантирован, и сужение потеряло бы
// записи. Порядок args обязан совпадать с порядком conds, иначе значения уйдут
// не в те плейсхолдеры.
func TestSearchConds_TimeWindow(t *testing.T) {
	t.Parallel()

	var (
		sinceMs = time.Date(2026, 7, 21, 10, 20, 29, 0, time.UTC).UnixMilli()
		untilMs = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC).UnixMilli()
	)
	const (
		condSince  = "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) > ?"
		condUntil  = "toUnixTimestamp64Milli(toDateTime64(date_request, 3)) <= ?"
		condKeyset = "(toUnixTimestamp64Milli(toDateTime64(date_request, 3)) < ? " +
			"OR (toUnixTimestamp64Milli(toDateTime64(date_request, 3)) = ? AND ID < ?))"
		condDayFrom = "date_create >= toDate(?)"
		condDayTo   = "date_create <= toDate(?)"
	)

	tests := []struct {
		name      string
		q         port.LogQuery
		wantConds []string
		wantArgs  []any
	}{
		{name: "без границ — условий нет", q: port.LogQuery{}},
		{
			name:      "внешняя таблица §64 — только date_request",
			q:         port.LogQuery{SinceMs: sinceMs, UntilMs: untilMs},
			wantConds: []string{condSince, condUntil},
			wantArgs:  []any{sinceMs, untilMs},
		},
		{
			name:      "обе границы — дубль по date_create с запасом ±1 день",
			q:         port.LogQuery{SinceMs: sinceMs, UntilMs: untilMs, DateCreateAligned: true},
			wantConds: []string{condDayFrom, condDayTo, condSince, condUntil},
			wantArgs:  []any{"2026-07-20", "2026-07-30", sinceMs, untilMs},
		},
		{
			name:      "только нижняя граница",
			q:         port.LogQuery{SinceMs: sinceMs, DateCreateAligned: true},
			wantConds: []string{condDayFrom, condSince},
			wantArgs:  []any{"2026-07-20", sinceMs},
		},
		{
			name:      "без временных границ сужать нечем",
			q:         port.LogQuery{DateCreateAligned: true},
			wantConds: nil,
			wantArgs:  nil,
		},
		{
			name:      "keyset-курсор перешагивает плотную секунду",
			q:         port.LogQuery{UntilMs: untilMs, BeforeID: "id-42", DateCreateAligned: true},
			wantConds: []string{condDayTo, condKeyset},
			wantArgs:  []any{"2026-07-30", untilMs, untilMs, "id-42"},
		},
		{
			name: "курсор вместе с нижней границей — порядок args сохранён",
			q: port.LogQuery{
				SinceMs: sinceMs, UntilMs: untilMs, BeforeID: "id-42", DateCreateAligned: true,
			},
			wantConds: []string{condDayFrom, condDayTo, condSince, condKeyset},
			wantArgs:  []any{"2026-07-20", "2026-07-30", sinceMs, untilMs, untilMs, "id-42"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := NewLogReader(nil, logging.NewNoop())
			conds, args := r.searchConds(context.Background(), tt.q)
			assert.Equal(t, tt.wantConds, conds)
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

// TestSearchConds_StatusComplement — инвариант дизъюнктности на уровне SQL:
// условие «Ошибки» обязано быть текстовым отрицанием условия «ОК». Ловит
// правку одной ветки switch без второй (тогда часть записей выпала бы из обоих
// фильтров либо попала в оба).
func TestSearchConds_StatusComplement(t *testing.T) {
	t.Parallel()

	r := NewLogReader(nil, logging.NewNoop())
	okConds, _ := r.searchConds(context.Background(), port.LogQuery{Status: "ok"})
	errConds, _ := r.searchConds(context.Background(), port.LogQuery{Status: "err"})

	require.Len(t, okConds, 1)
	require.Len(t, errConds, 1)
	assert.Equal(t, "NOT "+okConds[0], errConds[0])
}
