package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// chFullTableNamePattern — "<db>.<table>", обе части [A-Za-z0-9_], ровно одна
// точка. Совпадает с fullTableNamePattern в clickhouse-адаптере: имя идёт в DDL
// напрямую (CH не принимает параметризованные имена), поэтому валидируется здесь.
var chFullTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// RenderCreateTable детерминированно собирает `CREATE TABLE IF NOT EXISTS`
// из шаблона (§19.3). Все 20 обязательных колонок берутся из RequiredLogColumns
// в фиксированном порядке; CODEC/индексы/TTL — из шаблона. Имя таблицы и ttlDays
// передаются параметрами (а не плейсхолдерами) и валидируются — защита от инъекции.
func (t *CHTemplate) RenderCreateTable(table string, ttlDays int32) (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	if !chFullTableNamePattern.MatchString(table) {
		return "", ErrCHTemplateInvalidTableName
	}
	if t.Spec.TTLMode == CHTTLModeTTLDays && ttlDays <= 0 {
		return "", ErrCHTemplateTTLDaysRequired
	}

	codecByColumn := make(map[string]string, len(t.Spec.ColumnOverrides))
	for _, o := range t.Spec.ColumnOverrides {
		codecByColumn[o.Name] = o.Codec
	}

	lines := make([]string, 0, len(RequiredLogColumns)+len(t.Spec.Indexes))
	for _, c := range RequiredLogColumns {
		line := "    " + c.Name + " " + c.Type
		if codec := codecByColumn[c.Name]; codec != "" {
			line += " CODEC(" + codec + ")"
		}
		lines = append(lines, line)
	}
	for _, idx := range t.Spec.Indexes {
		lines = append(lines, fmt.Sprintf("    INDEX %s %s TYPE %s GRANULARITY %d",
			idx.Name, idx.Expr, idx.Type, idx.Granularity))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE IF NOT EXISTS %s (\n", table)
	b.WriteString(strings.Join(lines, ",\n"))
	b.WriteString("\n) ENGINE = ")
	b.WriteString(string(t.Spec.Engine))
	b.WriteString("\nPARTITION BY ")
	b.WriteString(t.Spec.PartitionBy)
	b.WriteString("\nORDER BY (")
	b.WriteString(strings.Join(t.Spec.OrderBy, ", "))
	b.WriteByte(')')
	if t.Spec.TTLMode == CHTTLModeTTLDays {
		fmt.Fprintf(&b, "\nTTL date_create + INTERVAL %d DAY DELETE", ttlDays)
	}
	return b.String(), nil
}
