package domain

import (
	"regexp"
	"strings"
	"time"
)

// CHEngine — движок таблицы логов. В v1 поддерживается только MergeTree
// (Sender пишет батч без dedup-семантики Replacing/Aggregating).
type CHEngine string

const CHEngineMergeTree CHEngine = "MergeTree"

// CHTTLMode — режим хранения данных в шаблоне.
//   - none     — TTL не выставляется; retention обеспечивает ch_housekeeping
//     (partition-drop по nodes.clickhouse_retention_days).
//   - ttl_days — native CH TTL `date_create + INTERVAL <ttl_days> DAY DELETE`.
type CHTTLMode string

const (
	CHTTLModeNone    CHTTLMode = "none"
	CHTTLModeTTLDays CHTTLMode = "ttl_days"
)

// CHTemplate — шаблон DDL для таблицы логов узла (§19). Хранит структурное
// описание, из которого детерминированно генерируется CREATE TABLE
// (см. RenderCreateTable). Глобальный каталог (не per-team): имя БД
// подставляется при применении к узлу конкретной команды.
type CHTemplate struct {
	ID          string
	Name        string
	Description string
	Spec        CHTemplateSpec
	IsDefault   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CHTemplateSpec — сериализуемое (JSONB) тело шаблона.
type CHTemplateSpec struct {
	Engine          CHEngine           `json:"engine"`
	PartitionBy     string             `json:"partition_by"`
	OrderBy         []string           `json:"order_by"`
	ColumnOverrides []CHColumnOverride `json:"column_overrides,omitempty"`
	Indexes         []CHTemplateIndex  `json:"indexes,omitempty"`
	TTLMode         CHTTLMode          `json:"ttl_mode"`
}

// CHColumnOverride — настройка сжатия (CODEC) для обязательной колонки.
type CHColumnOverride struct {
	Name  string `json:"name"`  // имя колонки из RequiredLogColumns
	Codec string `json:"codec"` // содержимое CODEC(...), напр. "ZSTD(3)" или "Delta, ZSTD"
}

// CHTemplateIndex — data-skipping индекс таблицы.
type CHTemplateIndex struct {
	Name        string `json:"name"`        // [A-Za-z0-9_]+
	Expr        string `json:"expr"`        // имя обязательной колонки
	Type        string `json:"type"`        // minmax | set(N) | bloom_filter(...) | tokenbf_v1(...) | ngrambf_v1(...)
	Granularity int32  `json:"granularity"` // GRANULARITY N
}

// chPartitionWhitelist — допустимые выражения PARTITION BY (анти-инъекция:
// строка идёт в DDL напрямую, параметризации имён в CH нет).
var chPartitionWhitelist = map[string]bool{
	"toYYYYMM(date_create)":   true,
	"toYYYYMMDD(date_create)": true,
	"toDate(date_create)":     true,
	"tuple()":                 true,
}

var (
	chTemplateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$`)
	chIndexNamePattern    = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	chCodecTokenPattern   = regexp.MustCompile(`^(NONE|LZ4|LZ4HC(\([0-9]{1,2}\))?|ZSTD(\([0-9]{1,2}\))?|Delta(\([0-9]{1,2}\))?|DoubleDelta|Gorilla|T64)$`)
	chIndexTypePattern    = regexp.MustCompile(`^(minmax|set\([0-9]+\)|bloom_filter(\(0?\.[0-9]+\))?|tokenbf_v1\([0-9]+, ?[0-9]+, ?[0-9]+\)|ngrambf_v1\([0-9]+, ?[0-9]+, ?[0-9]+, ?[0-9]+\))$`)
)

// Validate проверяет инварианты шаблона (статическая верификация §19.4).
// Гарантирует, что DDL соберётся из 20 обязательных колонок, а пользовательские
// CODEC/индексы/PARTITION/ORDER безопасны для прямой подстановки в SQL.
func (t *CHTemplate) Validate() error {
	if !chTemplateNamePattern.MatchString(t.Name) {
		return ErrCHTemplateNameFormat
	}
	if len(t.Description) > 1000 {
		return ErrCHTemplateDescriptionLength
	}
	return t.Spec.Validate()
}

// Validate проверяет тело шаблона.
func (s *CHTemplateSpec) Validate() error {
	if s.Engine != CHEngineMergeTree {
		return ErrCHTemplateInvalidEngine
	}
	if !chPartitionWhitelist[s.PartitionBy] {
		return ErrCHTemplateInvalidPartition
	}
	if len(s.OrderBy) == 0 {
		return ErrCHTemplateEmptyOrderBy
	}
	for _, col := range s.OrderBy {
		if !IsRequiredLogColumn(col) {
			return ErrCHTemplateInvalidOrderBy
		}
	}
	for _, o := range s.ColumnOverrides {
		if !IsRequiredLogColumn(o.Name) {
			return ErrCHTemplateUnknownColumn
		}
		if !validCHCodec(o.Codec) {
			return ErrCHTemplateInvalidCodec
		}
	}
	for _, idx := range s.Indexes {
		if !chIndexNamePattern.MatchString(idx.Name) {
			return ErrCHTemplateInvalidIndexName
		}
		if !IsRequiredLogColumn(idx.Expr) {
			return ErrCHTemplateInvalidIndexExpr
		}
		if !chIndexTypePattern.MatchString(idx.Type) {
			return ErrCHTemplateInvalidIndexType
		}
		if idx.Granularity < 1 || idx.Granularity > 1_000_000 {
			return ErrCHTemplateInvalidIndexGranularity
		}
	}
	if s.TTLMode != CHTTLModeNone && s.TTLMode != CHTTLModeTTLDays {
		return ErrCHTemplateInvalidTTLMode
	}
	return nil
}

// validCHCodec разрешает один токен или их цепочку через запятую
// ("Delta, ZSTD(3)") — каждый токен из белого списка.
func validCHCodec(codec string) bool {
	if codec == "" {
		return false
	}
	for tok := range strings.SplitSeq(codec, ",") {
		if !chCodecTokenPattern.MatchString(strings.TrimSpace(tok)) {
			return false
		}
	}
	return true
}

// DefaultCHTemplateSpec — спецификация шаблона «Standard logs», рендерящаяся
// ровно в схему §4.3 (MergeTree, PARTITION BY toYYYYMM(date_create),
// ORDER BY (date_create, date_request, method), без CODEC/индексов/TTL).
// Используется сидом миграции 0009 и golden-тестом.
func DefaultCHTemplateSpec() CHTemplateSpec {
	return CHTemplateSpec{
		Engine:      CHEngineMergeTree,
		PartitionBy: "toYYYYMM(date_create)",
		OrderBy:     []string{"date_create", "date_request", "method"},
		TTLMode:     CHTTLModeNone,
	}
}
