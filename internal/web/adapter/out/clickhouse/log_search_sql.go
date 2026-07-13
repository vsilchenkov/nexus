package clickhouse

import (
	"fmt"
	"strings"

	"nexus/internal/domain/logsearch"
)

// fieldColumns — whitelist logsearch.Field → CH-колонки. Имена колонок
// фиксированы, пользовательский ввод в идентификаторы не попадает.
// FieldAll разворачивается во все четыре колонки поиска (§48.1); field-scoped
// терм читает ровно одну колонку — тела request/response не сканируются
// (§48.5, основная экономия точечного поиска).
func fieldColumns(f logsearch.Field) []string {
	switch f {
	case logsearch.FieldURL:
		return []string{"url"}
	case logsearch.FieldParams:
		return []string{"parameters"}
	case logsearch.FieldRequest:
		return []string{"request"}
	case logsearch.FieldResponse:
		return []string{"response"}
	default:
		return []string{"url", "parameters", "request", "response"}
	}
}

// exprConds — WHERE-условие для распарсенного поискового выражения (§48) и
// его позиционные аргументы (в порядке появления `?`). Семантику зеркалит
// logsearch.Expr.Match (live-tail) — единый источник обоих представлений
// один пакет logsearch, менять согласованно.
func exprConds(e *logsearch.Expr) (string, []any) {
	groups := make([]string, 0, len(e.Groups))
	var args []any
	for _, g := range e.Groups {
		terms := make([]string, 0, len(g))
		for i := range g {
			c, a := termCond(&g[i], e)
			terms = append(terms, c)
			args = append(args, a...)
		}
		groups = append(groups, strings.Join(terms, " AND "))
	}
	if len(groups) == 1 {
		return "(" + groups[0] + ")", args
	}
	return "((" + strings.Join(groups, ") OR (") + "))", args
}

// termCond — условие одного терма: position*-подстрока (plain) либо match()
// (word/regex — Pattern уже несёт (?i)/\b, собран в logsearch.Parse). RE2 у
// Go regexp и CH match() совпадают — семантика snapshot↔live идентична.
func termCond(t *logsearch.Term, e *logsearch.Expr) (string, []any) {
	tpl, arg := "positionCaseInsensitiveUTF8(%s, ?) > 0", t.Text
	switch {
	case t.Pattern != "":
		tpl, arg = "match(%s, ?)", t.Pattern
	case e.CaseSensitive:
		tpl = "positionUTF8(%s, ?) > 0"
	}
	cols := fieldColumns(t.Field)
	parts := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf(tpl, col)
		args[i] = arg
	}
	cond := "(" + strings.Join(parts, " OR ") + ")"
	if t.Negate {
		cond = "NOT " + cond
	}
	return cond, args
}
