package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestRequiredLogColumns_MatchSpec фиксирует обязательную схему §4.3: 20
// колонок в точном порядке и с точными типами. Если кто-то поменяет
// RequiredLogColumns, разойдясь с insertSQL/selectCols/createNodeLogTable —
// тест упадёт.
func TestRequiredLogColumns_MatchSpec(t *testing.T) {
	want := []CHLogColumn{
		{"ID", "String"}, {"type", "String"}, {"url", "String"}, {"method", "String"},
		{"parameters", "String"}, {"request", "String"}, {"response", "String"},
		{"status", "Int32"}, {"reason", "String"}, {"date_create", "Date"},
		{"date_request", "DateTime"}, {"date_response", "DateTime"}, {"duration", "Int32"},
		{"done", "UInt8"}, {"checksum_request", "FixedString(32)"},
		{"checksum_response", "FixedString(32)"}, {"Host", "String"}, {"IP", "String"},
		{"attempts", "Int32"}, {"attempts_details", "String"},
	}
	if len(RequiredLogColumns) != len(want) {
		t.Fatalf("len = %d, want %d", len(RequiredLogColumns), len(want))
	}
	for i, c := range want {
		if RequiredLogColumns[i] != c {
			t.Errorf("col[%d] = %+v, want %+v", i, RequiredLogColumns[i], c)
		}
	}
}

func defaultTemplate() *CHTemplate {
	return &CHTemplate{Name: "Standard logs", Spec: DefaultCHTemplateSpec()}
}

func TestCHTemplate_Validate_OK(t *testing.T) {
	tpl := defaultTemplate()
	tpl.Spec.ColumnOverrides = []CHColumnOverride{
		{Name: "request", Codec: "ZSTD(3)"},
		{Name: "response", Codec: "Delta, ZSTD"},
	}
	tpl.Spec.Indexes = []CHTemplateIndex{
		{Name: "idx_host", Expr: "Host", Type: "bloom_filter(0.01)", Granularity: 4},
		{Name: "idx_status", Expr: "status", Type: "minmax", Granularity: 1},
	}
	if err := tpl.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCHTemplate_Validate_Errors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*CHTemplate)
		want error
	}{
		{"bad name", func(c *CHTemplate) { c.Name = "" }, ErrCHTemplateNameFormat},
		{"name with dot", func(c *CHTemplate) { c.Name = "bad.name" }, ErrCHTemplateNameFormat},
		{"bad engine", func(c *CHTemplate) { c.Spec.Engine = "ReplacingMergeTree" }, ErrCHTemplateInvalidEngine},
		{"bad partition", func(c *CHTemplate) { c.Spec.PartitionBy = "toYYYYMM(x); DROP" }, ErrCHTemplateInvalidPartition},
		{"empty order by", func(c *CHTemplate) { c.Spec.OrderBy = nil }, ErrCHTemplateEmptyOrderBy},
		{"order by unknown col", func(c *CHTemplate) { c.Spec.OrderBy = []string{"evil"} }, ErrCHTemplateInvalidOrderBy},
		{"override unknown col", func(c *CHTemplate) {
			c.Spec.ColumnOverrides = []CHColumnOverride{{Name: "evil", Codec: "LZ4"}}
		}, ErrCHTemplateUnknownColumn},
		{"bad codec", func(c *CHTemplate) {
			c.Spec.ColumnOverrides = []CHColumnOverride{{Name: "request", Codec: "ZSTD(3)); DROP TABLE x"}}
		}, ErrCHTemplateInvalidCodec},
		{"bad index name", func(c *CHTemplate) {
			c.Spec.Indexes = []CHTemplateIndex{{Name: "a b", Expr: "Host", Type: "minmax", Granularity: 1}}
		}, ErrCHTemplateInvalidIndexName},
		{"bad index expr", func(c *CHTemplate) {
			c.Spec.Indexes = []CHTemplateIndex{{Name: "i", Expr: "evil", Type: "minmax", Granularity: 1}}
		}, ErrCHTemplateInvalidIndexExpr},
		{"bad index type", func(c *CHTemplate) {
			c.Spec.Indexes = []CHTemplateIndex{{Name: "i", Expr: "Host", Type: "evil()", Granularity: 1}}
		}, ErrCHTemplateInvalidIndexType},
		{"bad granularity", func(c *CHTemplate) {
			c.Spec.Indexes = []CHTemplateIndex{{Name: "i", Expr: "Host", Type: "minmax", Granularity: 0}}
		}, ErrCHTemplateInvalidIndexGranularity},
		{"bad ttl mode", func(c *CHTemplate) { c.Spec.TTLMode = "forever" }, ErrCHTemplateInvalidTTLMode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := defaultTemplate()
			c.mut(tpl)
			err := tpl.Validate()
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// TestCHTemplate_RenderCreateTable_Default — golden DDL дефолтного шаблона.
// Доказывает, что шаблонная генерация совместима со схемой §4.3
// (createNodeLogTable в tests/integration/clickhouse_test.go).
func TestCHTemplate_RenderCreateTable_Default(t *testing.T) {
	const want = `CREATE TABLE IF NOT EXISTS nexus_default.webhook_send (
    ID String,
    type String,
    url String,
    method String,
    parameters String,
    request String,
    response String,
    status Int32,
    reason String,
    date_create Date,
    date_request DateTime,
    date_response DateTime,
    duration Int32,
    done UInt8,
    checksum_request FixedString(32),
    checksum_response FixedString(32),
    Host String,
    IP String,
    attempts Int32,
    attempts_details String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(date_create)
ORDER BY (date_create, date_request, method)`

	got, err := defaultTemplate().RenderCreateTable("nexus_default.webhook_send", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("DDL mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestCHTemplate_RenderCreateTable_OverridesIndexTTL(t *testing.T) {
	tpl := defaultTemplate()
	tpl.Spec.ColumnOverrides = []CHColumnOverride{{Name: "request", Codec: "ZSTD(3)"}}
	tpl.Spec.Indexes = []CHTemplateIndex{{Name: "idx_host", Expr: "Host", Type: "bloom_filter(0.01)", Granularity: 4}}
	tpl.Spec.TTLMode = CHTTLModeTTLDays

	got, err := tpl.RenderCreateTable("nexus_default.t", 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{
		"request String CODEC(ZSTD(3))",
		"INDEX idx_host Host TYPE bloom_filter(0.01) GRANULARITY 4",
		"TTL date_create + INTERVAL 30 DAY DELETE",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("DDL missing %q\n%s", sub, got)
		}
	}
}

func TestCHTemplate_RenderCreateTable_Errors(t *testing.T) {
	cases := []struct {
		name    string
		table   string
		ttlDays int32
		mut     func(*CHTemplate)
		want    error
	}{
		{"injection in table name", "nexus_default.t; DROP TABLE x", 0, nil, ErrCHTemplateInvalidTableName},
		{"no db prefix", "webhook_send", 0, nil, ErrCHTemplateInvalidTableName},
		{"ttl_days without days", "nexus_default.t", 0, func(c *CHTemplate) { c.Spec.TTLMode = CHTTLModeTTLDays }, ErrCHTemplateTTLDaysRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := defaultTemplate()
			if c.mut != nil {
				c.mut(tpl)
			}
			_, err := tpl.RenderCreateTable(c.table, c.ttlDays)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}
