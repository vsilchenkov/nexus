package http

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
}

// PublicSettingsResponse — GET /api/settings/public.
type PublicSettingsResponse struct {
	PublicBaseURL string `json:"public_base_url"`
	// §44.C: интервал автообновления метрик (мс) для дашборда/страниц узлов.
	MetricsRefetchMs int `json:"metrics_refetch_ms"`
}

// UserEnvelope — обёртка {"user": ...} для login/me.
type UserEnvelope struct {
	User meResponse `json:"user"`
}

// MyTeamsResponse — GET /api/me/teams.
type MyTeamsResponse struct {
	Items         []teamMembershipResponse `json:"items"`
	CurrentTeamID string                   `json:"current_team_id"`
}

// SwitchTeamResponse — POST /api/me/switch-team.
type SwitchTeamResponse struct {
	CurrentTeamID string `json:"current_team_id"`
}

// NodesMetricsResponse — GET /api/metrics/nodes.
type NodesMetricsResponse struct {
	Items               []nodeThroughputDTO `json:"items"`
	PrometheusAvailable bool                `json:"prometheus_available"`
}

// NodeMetricsResponse — GET /api/metrics/nodes/{id}.
type NodeMetricsResponse struct {
	KPI            nodeKPIDTO       `json:"kpi"`
	Series         []seriesPointDTO `json:"series"`
	ChartAvailable bool             `json:"chart_available"`
	RangeMs        int64            `json:"range_ms"`
}

// KafkaTopicsResponse — GET /api/kafka/topics.
type KafkaTopicsResponse struct {
	Topics         []kafkaTopicDTO `json:"topics"`
	KafkaAvailable bool            `json:"kafka_available"`
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
