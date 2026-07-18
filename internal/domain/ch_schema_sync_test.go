package domain

import (
	"strings"
	"testing"
)

const syncTable = "nexus_default.logs"

func stdSpec() CHTemplateSpec {
	return CHTemplateSpec{
		Engine:      CHEngineMergeTree,
		PartitionBy: "toYYYYMM(date_create)",
		OrderBy:     []string{"date_create", "date_request", "method"},
		TTLMode:     CHTTLModeNone,
	}
}

// matchingCurrent — снимок таблицы, точно соответствующий stdSpec (для проверки
// «пустого» плана и добавления частных изменений поверх).
func matchingCurrent() CurrentTableSchema {
	return CurrentTableSchema{
		Codecs:      map[string]string{},
		Engine:      "MergeTree",
		OrderBy:     []string{"date_create", "date_request", "method"},
		PartitionBy: "toYYYYMM(date_create)",
	}
}

func hasStmt(stmts []string, substr string) bool {
	for _, s := range stmts {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestPlanSchemaSync_NoChanges(t *testing.T) {
	t.Parallel()
	p := PlanSchemaSync(syncTable, stdSpec(), 0, matchingCurrent())
	if !p.Empty() {
		t.Fatalf("want empty plan, got %+v", p)
	}
}

func TestPlanSchemaSync_Codec(t *testing.T) {
	t.Parallel()

	t.Run("add codec", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.ColumnOverrides = []CHColumnOverride{{Name: "request", Codec: "ZSTD(3)"}}
		p := PlanSchemaSync(syncTable, spec, 0, matchingCurrent())
		want := "ALTER TABLE nexus_default.logs MODIFY COLUMN request String CODEC(ZSTD(3))"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q]", p.Statements, want)
		}
	})

	t.Run("remove codec", func(t *testing.T) {
		t.Parallel()
		cur := matchingCurrent()
		cur.Codecs["request"] = "ZSTD(3)"
		p := PlanSchemaSync(syncTable, stdSpec(), 0, cur)
		want := "ALTER TABLE nexus_default.logs MODIFY COLUMN request String"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q] (без CODEC — сброс к дефолту)", p.Statements, want)
		}
	})

	t.Run("unchanged codec with CODEC() wrapper normalizes", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.ColumnOverrides = []CHColumnOverride{{Name: "request", Codec: "ZSTD(3)"}}
		cur := matchingCurrent()
		cur.Codecs["request"] = "CODEC(ZSTD(3))" // как отдаёт ClickHouse
		p := PlanSchemaSync(syncTable, spec, 0, cur)
		if !p.Empty() {
			t.Fatalf("codec совпадает после нормализации — план должен быть пуст, got %+v", p)
		}
	})
}

func TestPlanSchemaSync_Indexes(t *testing.T) {
	t.Parallel()

	idx := CHTemplateIndex{Name: "idx_status", Expr: "status", Type: "minmax", Granularity: 4}

	t.Run("add index", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.Indexes = []CHTemplateIndex{idx}
		p := PlanSchemaSync(syncTable, spec, 0, matchingCurrent())
		want := "ALTER TABLE nexus_default.logs ADD INDEX idx_status status TYPE minmax GRANULARITY 4"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q]", p.Statements, want)
		}
	})

	t.Run("drop index", func(t *testing.T) {
		t.Parallel()
		cur := matchingCurrent()
		cur.Indexes = []CurrentIndex{{Name: "idx_status", Type: "minmax", Expr: "status", Granularity: 4}}
		p := PlanSchemaSync(syncTable, stdSpec(), 0, cur)
		want := "ALTER TABLE nexus_default.logs DROP INDEX idx_status"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q]", p.Statements, want)
		}
	})

	t.Run("changed granularity → drop then add", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.Indexes = []CHTemplateIndex{idx} // granularity 4
		cur := matchingCurrent()
		cur.Indexes = []CurrentIndex{{Name: "idx_status", Type: "minmax", Expr: "status", Granularity: 1}}
		p := PlanSchemaSync(syncTable, spec, 0, cur)
		if len(p.Statements) != 2 ||
			!strings.HasPrefix(p.Statements[0], "ALTER TABLE nexus_default.logs DROP INDEX idx_status") ||
			!strings.HasPrefix(p.Statements[1], "ALTER TABLE nexus_default.logs ADD INDEX idx_status") {
			t.Fatalf("want DROP then ADD, got %+v", p.Statements)
		}
	})

	t.Run("unchanged index → no statement", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.Indexes = []CHTemplateIndex{idx}
		cur := matchingCurrent()
		cur.Indexes = []CurrentIndex{{Name: "idx_status", Type: "minmax", Expr: "status", Granularity: 4}}
		p := PlanSchemaSync(syncTable, spec, 0, cur)
		if !p.Empty() {
			t.Fatalf("index совпадает — план пуст, got %+v", p)
		}
	})
}

func TestPlanSchemaSync_TTL(t *testing.T) {
	t.Parallel()

	t.Run("add ttl", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.TTLMode = CHTTLModeTTLDays
		p := PlanSchemaSync(syncTable, spec, 30, matchingCurrent())
		want := "ALTER TABLE nexus_default.logs MODIFY TTL date_create + INTERVAL 30 DAY DELETE"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q]", p.Statements, want)
		}
	})

	t.Run("change ttl days", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.TTLMode = CHTTLModeTTLDays
		cur := matchingCurrent()
		cur.HasTTL = true
		cur.TTLDays = 30
		p := PlanSchemaSync(syncTable, spec, 90, cur)
		if !hasStmt(p.Statements, "MODIFY TTL date_create + INTERVAL 90 DAY DELETE") {
			t.Fatalf("want MODIFY TTL 90, got %+v", p.Statements)
		}
	})

	t.Run("same ttl days → no statement", func(t *testing.T) {
		t.Parallel()
		spec := stdSpec()
		spec.TTLMode = CHTTLModeTTLDays
		cur := matchingCurrent()
		cur.HasTTL = true
		cur.TTLDays = 30
		p := PlanSchemaSync(syncTable, spec, 30, cur)
		if !p.Empty() {
			t.Fatalf("TTL совпадает — план пуст, got %+v", p)
		}
	})

	t.Run("remove ttl", func(t *testing.T) {
		t.Parallel()
		cur := matchingCurrent()
		cur.HasTTL = true
		cur.TTLDays = 30
		p := PlanSchemaSync(syncTable, stdSpec(), 0, cur) // TTLMode none
		want := "ALTER TABLE nexus_default.logs REMOVE TTL"
		if len(p.Statements) != 1 || p.Statements[0] != want {
			t.Fatalf("got %+v, want [%q]", p.Statements, want)
		}
	})
}

// Неприменимые ALTER'ом изменения (движок/ORDER BY/PARTITION BY) → rejections,
// но применимые (CODEC) при этом всё равно планируются.
func TestPlanSchemaSync_Rejections(t *testing.T) {
	t.Parallel()
	spec := stdSpec()
	spec.OrderBy = []string{"date_create"} // отличается от текущего
	spec.PartitionBy = "toDate(date_create)"
	spec.ColumnOverrides = []CHColumnOverride{{Name: "request", Codec: "ZSTD(3)"}}
	cur := matchingCurrent() // engine MergeTree совпадает, order/partition отличаются

	p := PlanSchemaSync(syncTable, spec, 0, cur)

	kinds := map[string]bool{}
	for _, r := range p.Rejections {
		kinds[r.Kind] = true
	}
	if !kinds["order_by"] || !kinds["partition_by"] {
		t.Fatalf("want order_by+partition_by rejections, got %+v", p.Rejections)
	}
	if kinds["engine"] {
		t.Fatalf("engine совпадает — не должно быть rejection: %+v", p.Rejections)
	}
	if !hasStmt(p.Statements, "MODIFY COLUMN request String CODEC(ZSTD(3))") {
		t.Fatalf("применимый CODEC должен планироваться несмотря на rejections, got %+v", p.Statements)
	}
}

// Неполный снимок (Engine/OrderBy/PartitionBy неизвестны) не порождает
// rejection — иначе частичная интроспекция блокировала бы применимые ALTER'ы.
func TestPlanSchemaSync_UnknownSnapshotNoRejection(t *testing.T) {
	t.Parallel()
	cur := CurrentTableSchema{Codecs: map[string]string{}} // всё «неизвестно»
	p := PlanSchemaSync(syncTable, stdSpec(), 0, cur)
	if len(p.Rejections) != 0 {
		t.Fatalf("неизвестный снимок не должен давать rejections, got %+v", p.Rejections)
	}
}
