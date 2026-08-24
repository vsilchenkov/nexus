package http

import (
	"encoding/json"
	"time"
)

// Общие DTO для Swagger-моделей (§11, §25 ТЗ; QA-2026-02 / П10).
//
// До этого все handler'ы декларировали ответы как `map[string]any` /
// `map[string]string`, из-за чего Swagger UI показывал пустую/генерик-модель
// («additionalProp1: string»). Конкретные DTO ниже дают читаемую схему.
// На runtime-поведение не влияют — handler'ы по-прежнему отдают gin.H.

// ErrorResponse — стандартное тело ошибки: {"error": "<сообщение>"}.
// Опциональный code заполняется там, где фронт реагирует машинно
// (например password_change_required).
type ErrorResponse struct {
	Error string `json:"error" example:"not found"`
	Code  string `json:"code,omitempty" example:"password_change_required"`
}

// --- List/object DTO для Swagger (зеркалят gin.H, отдаваемые handler'ами) ---

// ListNodesResponse — GET /api/nodes.
type ListNodesResponse struct {
	Items []NodeResponse `json:"items"`
}

// ListUsersResponse — GET /api/users.
type ListUsersResponse struct {
	Items []userResponse `json:"items"`
}

// ListTeamsResponse — GET /api/teams.
type ListTeamsResponse struct {
	Items []teamResponse `json:"items"`
}

// ListTeamMembersResponse — GET /api/teams/{id}/members.
type ListTeamMembersResponse struct {
	Items []teamMemberResponse `json:"items"`
}

// ListTokensResponse — GET /api/tokens.
type ListTokensResponse struct {
	Items []tokenResponse `json:"items"`
}

// CreateTokenResponse — POST /api/tokens (plain-токен показывается один раз).
type CreateTokenResponse struct {
	Token string        `json:"token"`
	Info  tokenResponse `json:"info"`
}

// RotateTokenResponse — POST /api/tokens/{id}/rotate (новое значение показывается
// один раз; данные токена UI перечитывает списком).
type RotateTokenResponse struct {
	Token string `json:"token"`
}

// ListAuditResponse — GET /api/audit.
type ListAuditResponse struct {
	Items []auditEntryResponse `json:"items"`
}

// ListHostsResponse — GET /api/allowed-hosts и /api/nodes/{id}/allowed-hosts.
type ListHostsResponse struct {
	Items []hostResponse `json:"items"`
}

// HostPreviewResponse — POST /api/allowed-hosts/preview.
type HostPreviewResponse struct {
	Valid   bool     `json:"valid"`
	Allowed []string `json:"allowed"`
	Blocked []string `json:"blocked"`
}

// ListHeadersResponse — GET /api/headers.
type ListHeadersResponse struct {
	Items []headerResponse `json:"items"`
}

// ListRequestFieldsResponse — GET /api/request-fields (§41).
type ListRequestFieldsResponse struct {
	Items []requestFieldResponse `json:"items"`
}

// ListLogMasksResponse — GET /api/log-masks (§95).
type ListLogMasksResponse struct {
	Items []logMaskResponse `json:"items"`
}

// LogMaskPreviewResponse — POST /api/log-masks/preview (§95).
type LogMaskPreviewResponse struct {
	Valid   bool   `json:"valid"`
	Result  string `json:"result"`
	Matched bool   `json:"matched"`
}

// ListOrphansResponse — GET /api/settings/clickhouse/orphans.
type ListOrphansResponse struct {
	Items []orphanItemResp `json:"items"`
}

// ListCHTemplatesResponse — GET /api/ch-templates.
type ListCHTemplatesResponse struct {
	Items []chTemplateResponse `json:"items"`
}

// CHTemplateVerifyResponse — POST /api/ch-templates/verify.
type CHTemplateVerifyResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// ListLogsResponse — GET /api/nodes/{id}/logs.
type ListLogsResponse struct {
	Items          []LogRecordDTO `json:"items"`
	LogsConfigured bool           `json:"logs_configured,omitempty"`
}

// FailedCountResponse — GET /api/nodes/{id}/logs/failed-count (§35).
type FailedCountResponse struct {
	Count          uint64 `json:"count"`
	LogsConfigured bool   `json:"logs_configured"`
}

// LogMethodsResponse — GET /api/nodes/{id}/logs/methods (§48.3): уникальные
// значения колонки method узла для фасета дропдауна Method.
type LogMethodsResponse struct {
	Items          []string `json:"items"`
	LogsConfigured bool     `json:"logs_configured"`
	LogsAvailable  bool     `json:"logs_available"`
}

// LogClientHostsResponse — GET /api/nodes/{id}/logs/client-hosts (§67):
// уникальные значения колонки client_host узла для фасета дропдауна
// «Хост клиента».
type LogClientHostsResponse struct {
	Items          []string `json:"items"`
	LogsConfigured bool     `json:"logs_configured"`
	LogsAvailable  bool     `json:"logs_available"`
}

// LogCountResponse — GET /api/nodes/{id}/logs/count (§67): точное число
// записей под теми же фильтрами, что и список логов («Показано N из M»).
type LogCountResponse struct {
	Total          uint64 `json:"total"`
	LogsConfigured bool   `json:"logs_configured"`
	LogsAvailable  bool   `json:"logs_available"`
}

// AuditCountResponse — GET /api/audit/count (§91.1): сколько записей журнала
// подходит под фильтр целиком, для счётчика «показано N из M».
type AuditCountResponse struct {
	Count int `json:"count"`
}

// LogDateRangeResponse — GET /api/nodes/{id}/logs/date-range (§48.3): min/max
// date_request узла (UnixMilli) для ограничения полей дат фильтра. 0/0 —
// записей нет, ограничения не ставятся.
type LogDateRangeResponse struct {
	MinMs          int64 `json:"min_ms"`
	MaxMs          int64 `json:"max_ms"`
	LogsConfigured bool  `json:"logs_configured"`
	LogsAvailable  bool  `json:"logs_available"`
}

// LogBodyChunkResponse — GET /api/nodes/{id}/log/{logId}/body (§42): срез тела
// записи по рунам + полная длина для постраничной подгрузки «показать весь».
type LogBodyChunkResponse struct {
	Which    string `json:"which"`    // request | response
	Total    int64  `json:"total"`    // полная длина тела в рунах
	Offset   int    `json:"offset"`   // смещение среза в рунах
	Returned int    `json:"returned"` // сколько рун в chunk
	Chunk    string `json:"chunk"`    // сам срез
	EOF      bool   `json:"eof"`      // достигнут конец тела
}

// VersionResponse — GET /api/version (§34.3).
type VersionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	// OverrideAllowed — true в dev (web.allow_version_override): UI показывает
	// поле ручного override версии; в проде false (версия всегда из git).
	OverrideAllowed bool `json:"override_allowed"`
	// Instance — идентификатор ноды (§70.8). Пустой у ноды без идентификатора,
	// поэтому omitempty: интерфейс действующей ноды не меняется.
	Instance string `json:"instance,omitempty"`
	// DevMode — §85.8: установка тестовая (web.dev_mode). Единственный публичный
	// признак среды, доступный форме входа: /api/version — единственный /api без
	// авторизации, и SPA его уже запрашивает ради версии в футере.
	//
	// НЕ путать с OverrideAllowed: тот разрешает конкретную настройку §34.3.
	// Отдавать наружу dev_mode=false безопасно, true бывает только на стенде.
	DevMode bool `json:"dev_mode"`
	// PasswordResetReady — §88.4.5: восстановление пароля работоспособно
	// (почта включена и настроена, функция разрешена, задан публичный адрес).
	// Форма входа по нему решает, показывать ли ссылку «Забыли пароль?».
	//
	// Поле аддитивное: реестр инстансов §73 разбирает этот же ответ и от
	// нового ключа не ломается.
	PasswordResetReady bool `json:"password_reset_ready"`
}

// PublicSettingsResponse — GET /api/settings/public.
type PublicSettingsResponse struct {
	PublicBaseURL string `json:"public_base_url"`
	// §44.C: интервал автообновления метрик (мс) для дашборда/страниц узлов.
	MetricsRefetchMs int `json:"metrics_refetch_ms"`
	// §64: значение «Макс. размер тела», подставляемое формой при СОЗДАНИИ узла
	// (web.node_default_max_body_size). Существующие узлы не затрагивает.
	NodeDefaultMaxBodySize int `json:"node_default_max_body_size"`
	// §70.8: префикс имён БД ClickHouse этой ноды ("nexus_" либо "nexus_<id>_").
	// Диалог создания команды показывает предпросмотр имени БД из него, а не
	// склеивает литерал на клиенте — иначе на ноде с идентификатором предпросмотр
	// показывал бы чужое имя.
	CHDatabasePrefix string `json:"ch_database_prefix"`
}

// UserEnvelope — обёртка {"user": ...} для login/me.
type UserEnvelope struct {
	User meResponse `json:"user"`
}

// MyTeamsResponse — GET /api/me/teams.
type MyTeamsResponse struct {
	Items         []teamMembershipResponse `json:"items"`
	CurrentTeamID string                   `json:"current_team_id"`
	// Healed — §44.H: current_team в сессии был вне членств и переключён
	// сервером; UI по этому флагу инвалидирует team-scoped кеш.
	Healed bool `json:"healed"`
	// Favorites — §49: упорядоченный список id избранных команд пользователя.
	Favorites []string `json:"favorites"`
}

// FavoriteTeamsResponse — PUT /api/me/favorite-teams (§49): сохранённый
// упорядоченный список.
type FavoriteTeamsResponse struct {
	TeamIDs []string `json:"team_ids"`
}

// SwitchTeamResponse — POST /api/me/switch-team.
type SwitchTeamResponse struct {
	CurrentTeamID string `json:"current_team_id"`
}

// SearchHistoryResponse — GET /api/me/search-history (§62): последние строки
// поиска пользователя от свежих к старым (не более 10).
type SearchHistoryResponse struct {
	Items []string `json:"items"`
}

// UserPrefsResponse — GET /api/me/prefs (§71): все персональные предпочтения
// пользователя одним ответом — и глобальные, и по всем его командам. Клиент
// резолвит «преф команды → глобальный → системный дефолт» сам, поэтому набор
// отдаётся целиком, а не по текущей команде.
type UserPrefsResponse struct {
	Items []userPrefDTO `json:"items"`
}

// userPrefDTO — одна запись предпочтений. TeamID пустой = глобальный преф.
// Value — произвольный JSON: сервер значение не интерпретирует (§71.3).
// swaggertype:"object" обязателен — иначе swag описывает json.RawMessage
// как []integer (это []byte).
type userPrefDTO struct {
	TeamID    string          `json:"team_id"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value" swaggertype:"object"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// NodesMetricsResponse — GET /api/metrics/nodes.
type NodesMetricsResponse struct {
	Items []nodeThroughputDTO `json:"items"`
	// Totals — агрегат KPI шапки (§44.A): сумма строк items за тот же период.
	Totals              overviewTotalsDTO `json:"totals"`
	PrometheusAvailable bool              `json:"prometheus_available"`
}

// NodeMetricsResponse — GET /api/metrics/nodes/{id}.
type NodeMetricsResponse struct {
	KPI            nodeKPIDTO       `json:"kpi"`
	Series         []seriesPointDTO `json:"series"`
	ChartAvailable bool             `json:"chart_available"`
	RangeMs        int64            `json:"range_ms"`

	// StepSeconds — фактическая ширина столбца (§79.5). Может отличаться от
	// запрошенного step: шаг больше окна сжимается до окна, слишком мелкий —
	// поднимается до потолка столбцов. Клиенту брать ширину больше неоткуда:
	// на ряде из одной точки её не вывести из данных.
	StepSeconds int64 `json:"step_seconds"`

	// ChartUnit — как посчитаны столбцы (§79.5):
	//   records  — запись относится к интервалу своего прихода, «ошибка» по
	//              ИТОГОВОМУ статусу: после успешного повтора красный сегмент
	//              исчезает из столбца сам;
	//   attempts — деградация на больших окнах: запись считается в интервале
	//              своего прогона, статус — по прогонам интервала.
	ChartUnit string `json:"chart_unit"`
}

// KafkaTopicsResponse — GET /api/kafka/topics.
//
// SizesAvailable (§75): источник размеров (JMX-метрика kafka_log_log_size в
// Prometheus) ответил хотя бы одной серией. При false size_bytes у всех топиков
// нулевые «потому что источника нет»; при true нулевой size_bytes означает, что
// топик действительно пуст.
type KafkaTopicsResponse struct {
	Topics         []kafkaTopicDTO `json:"topics"`
	KafkaAvailable bool            `json:"kafka_available"`
	SizesAvailable bool            `json:"sizes_available"`
}

// KafkaByNodeResponse — GET /api/kafka/by-node.
type KafkaByNodeResponse struct {
	TopProducers        []kafkaProducerDTO `json:"top_producers"`
	TopFailures         []kafkaFailureDTO  `json:"top_failures"`
	PrometheusAvailable bool               `json:"prometheus_available"`
}

// KafkaTestResponse — POST /api/kafka/test.
type KafkaTestResponse struct {
	OK             bool                 `json:"ok"`
	Brokers        []kafkaBrokerPingDTO `json:"brokers"`
	KafkaAvailable bool                 `json:"kafka_available"`
}
