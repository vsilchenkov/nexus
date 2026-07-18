package domain

import (
	"fmt"
	"strings"
)

// §56: приведение схемы СУЩЕСТВУЮЩЕЙ таблицы логов к целевому шаблону через
// ALTER. Таблица создаётся один раз (CREATE TABLE IF NOT EXISTS), и смена
// шаблона/настроек к ней не применяется (§19, ловушка C.1). Здесь — чистый
// планировщик: диффит целевой CHTemplateSpec против снимка существующей таблицы
// и выдаёт ALTER-операторы для применимого + список неприменимого (нужна
// пересоздача). ClickHouse не трогает — исполнение отдельно (usecase/adapter).

// CurrentIndex — data-skipping индекс существующей таблицы (из
// system.data_skipping_indices).
type CurrentIndex struct {
	Name        string
	Type        string
	Expr        string
	Granularity int32
}

// CurrentTableSchema — снимок схемы существующей таблицы логов, прочитанный из
// ClickHouse. Левая сторона диффа против целевого шаблона (§56). Пустые поля
// (Engine/OrderBy/PartitionBy) трактуются как «неизвестно» и не порождают
// rejection — чтобы неполный снимок не блокировал применимые ALTER'ы.
type CurrentTableSchema struct {
	// Codecs — имя обязательной колонки → содержимое CODEC(...) без обёртки
	// (напр. "ZSTD(3)"); отсутствие/пусто = дефолтное сжатие.
	Codecs      map[string]string
	Indexes     []CurrentIndex
	HasTTL      bool
	TTLDays     int32
	Engine      string
	OrderBy     []string
	PartitionBy string
}

// SchemaSyncRejection — изменение, которое ClickHouse не даёт применить ALTER'ом
// (движок, ORDER BY, PARTITION BY): нужна пересоздача таблицы с новым именем.
type SchemaSyncRejection struct {
	Kind    string `json:"kind"` // engine | order_by | partition_by
	Current string `json:"current"`
	Desired string `json:"desired"`
	Reason  string `json:"reason"`
}

// SchemaSyncPlan — результат диффа: применимые ALTER-операторы (в порядке
// исполнения) и список неприменимого. Пустой план = таблица уже соответствует.
type SchemaSyncPlan struct {
	Statements []string              `json:"statements"`
	Rejections []SchemaSyncRejection `json:"rejections"`
}

// Empty — применять нечего и всё совпадает (ни ALTER'ов, ни отклонений).
func (p SchemaSyncPlan) Empty() bool {
	return len(p.Statements) == 0 && len(p.Rejections) == 0
}

// PlanSchemaSync строит план приведения существующей таблицы cur к целевому
// шаблону desired (§56). table — уже валидированное имя `db.table` (то же
// значение, что в RenderCreateTable), ttlDays — retention узла (для
// TTLModeTTLDays). Чистая функция: диффит, ClickHouse не трогает.
//
// Порядок ALTER'ов: сперва CODEC, затем DROP INDEX, затем ADD INDEX, затем TTL —
// DROP до ADD, чтобы пересоздать изменившийся индекс с тем же именем без коллизии.
func PlanSchemaSync(table string, desired CHTemplateSpec, ttlDays int32, cur CurrentTableSchema) SchemaSyncPlan {
	var p SchemaSyncPlan

	// 1. Неприменимое ALTER'ом → rejections (нужна пересоздача таблицы).
	if cur.Engine != "" && !strings.EqualFold(cur.Engine, string(desired.Engine)) {
		p.Rejections = append(p.Rejections, SchemaSyncRejection{
			Kind: "engine", Current: cur.Engine, Desired: string(desired.Engine),
			Reason: "движок таблицы нельзя изменить ALTER'ом",
		})
	}
	if len(cur.OrderBy) > 0 && !equalStringSlice(cur.OrderBy, desired.OrderBy) {
		p.Rejections = append(p.Rejections, SchemaSyncRejection{
			Kind: "order_by", Current: strings.Join(cur.OrderBy, ", "), Desired: strings.Join(desired.OrderBy, ", "),
			Reason: "ORDER BY существующей таблицы нельзя изменить ALTER'ом",
		})
	}
	if cur.PartitionBy != "" && cur.PartitionBy != desired.PartitionBy {
		p.Rejections = append(p.Rejections, SchemaSyncRejection{
			Kind: "partition_by", Current: cur.PartitionBy, Desired: desired.PartitionBy,
			Reason: "PARTITION BY существующей таблицы нельзя изменить ALTER'ом",
		})
	}

	// 2. CODEC по обязательным колонкам (в фиксированном порядке RequiredLogColumns).
	desiredCodec := make(map[string]string, len(desired.ColumnOverrides))
	for _, o := range desired.ColumnOverrides {
		desiredCodec[o.Name] = o.Codec
	}
	for _, c := range RequiredLogColumns {
		want := desiredCodec[c.Name]
		if normalizeCodec(want) == normalizeCodec(cur.Codecs[c.Name]) {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", table, c.Name, c.Type)
		if want != "" {
			stmt += " CODEC(" + want + ")"
		}
		p.Statements = append(p.Statements, stmt)
	}

	// 3. Data-skipping индексы: DROP исчезнувшие/изменившиеся, ADD новые/изменившиеся.
	desIdx := make(map[string]CHTemplateIndex, len(desired.Indexes))
	for _, i := range desired.Indexes {
		desIdx[i.Name] = i
	}
	curIdx := make(map[string]CurrentIndex, len(cur.Indexes))
	for _, i := range cur.Indexes {
		curIdx[i.Name] = i
	}
	for _, i := range cur.Indexes {
		if d, ok := desIdx[i.Name]; !ok || !sameIndex(i, d) {
			p.Statements = append(p.Statements, fmt.Sprintf("ALTER TABLE %s DROP INDEX %s", table, i.Name))
		}
	}
	for _, i := range desired.Indexes {
		if c, ok := curIdx[i.Name]; ok && sameIndex(c, i) {
			continue
		}
		p.Statements = append(p.Statements, fmt.Sprintf(
			"ALTER TABLE %s ADD INDEX %s %s TYPE %s GRANULARITY %d",
			table, i.Name, i.Expr, i.Type, i.Granularity))
	}

	// 4. TTL. При ttl_days — MODIFY, если TTL нет или срок отличается; при none —
	// REMOVE, если TTL был. Гейт date_create + INTERVAL <n> DAY — как в CREATE.
	if desired.TTLMode == CHTTLModeTTLDays {
		if !cur.HasTTL || cur.TTLDays != ttlDays {
			p.Statements = append(p.Statements, fmt.Sprintf(
				"ALTER TABLE %s MODIFY TTL date_create + INTERVAL %d DAY DELETE", table, ttlDays))
		}
	} else if cur.HasTTL {
		p.Statements = append(p.Statements, fmt.Sprintf("ALTER TABLE %s REMOVE TTL", table))
	}

	return p
}

// normalizeCodec приводит запись CODEC к сравнимому виду: снимает обёртку
// `CODEC(...)` (ClickHouse отдаёт её в system.columns.compression_codec),
// убирает пробелы и регистр. Best-effort: семантически равные, но текстово
// разные кодеки (напр. `ZSTD` vs `ZSTD(1)` — дефолтный уровень) могут дать
// «ложный» MODIFY; ClickHouse исполнит его идемпотентно, оператор видит DDL в
// предпросмотре (§56).
func normalizeCodec(c string) string {
	c = strings.TrimSpace(c)
	if strings.HasPrefix(strings.ToUpper(c), "CODEC(") && strings.HasSuffix(c, ")") {
		c = c[len("CODEC(") : len(c)-1]
	}
	return strings.ToUpper(strings.ReplaceAll(c, " ", ""))
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameIndex(a CurrentIndex, b CHTemplateIndex) bool {
	return a.Type == b.Type && a.Expr == b.Expr && a.Granularity == b.Granularity
}
