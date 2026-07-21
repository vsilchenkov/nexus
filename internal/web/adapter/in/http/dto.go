// Package http — Gin handlers и DTO Web Service.
package http

import (
	"strings"
	"time"

	"nexus/internal/domain"
)

// basicLogin — логин из basic-кредов формата "login:password" (часть до
// ПЕРВОГО «:» — пароль может содержать двоеточия, логин по UI-валидации нет).
// Возвращается наружу только для basic (isBasic=false → пусто): у token это
// сам токен-секрет, у webhook_signature — HMAC-секрет, их светить нельзя.
// Легаси-креды без «:» трактуем как «логин без пароля» — отдаём целиком.
func basicLogin(isBasic bool, creds string) string {
	if !isBasic || creds == "" {
		return ""
	}
	login, _, _ := strings.Cut(creds, ":")
	return login
}

// CredentialsMask — что возвращаем вместо реального значения кредов.
// §5.5 ТЗ: «В UI значения никогда не возвращаются в API-ответах в открытом виде».
const CredentialsMask = "***"

// CreateNodeRequest — DTO для POST /api/nodes.
type CreateNodeRequest struct {
	Path                   string   `json:"path" binding:"required,max=255"`
	RootMethod             string   `json:"root_method" binding:"required,oneof=request requestAsync RabbitMQAsync"`
	IncomingMethod         string   `json:"incoming_method" binding:"omitempty,oneof=GET POST PUT DELETE ANY"`
	OutgoingMethod         string   `json:"outgoing_method" binding:"omitempty,oneof=GET POST PUT DELETE ANY"`
	URLMode                string   `json:"url_mode" binding:"omitempty,oneof=static from_request"`
	TargetURL              string   `json:"target_url" binding:"omitempty,max=2048"`
	URLParamName           string   `json:"url_param_name" binding:"omitempty,max=64"`
	URLAllowedHosts        []string `json:"url_allowed_hosts"`
	AuthType               string   `json:"auth_type" binding:"omitempty,oneof=none basic token token_from_request basic_from_request"`
	AuthCredentials        string   `json:"auth_credentials" binding:"omitempty,max=1024"`
	AuthDynamicSource      string   `json:"auth_dynamic_source" binding:"omitempty,oneof=query header body"`
	AuthDynamicField       string   `json:"auth_dynamic_field" binding:"omitempty,max=64"`
	AuthDynamicStripPrefix string   `json:"auth_dynamic_strip_prefix" binding:"omitempty,max=64"`
	IncomingAuthType       string   `json:"incoming_auth_type" binding:"omitempty,oneof=none basic token webhook_signature"`
	IncomingAuthCreds      string   `json:"incoming_auth_credentials" binding:"omitempty,max=1024"`
	// §41: источник и имя поля для входящей динамической авторизации.
	IncomingAuthDynamicSource string   `json:"incoming_auth_dynamic_source" binding:"omitempty,oneof=header query"`
	IncomingAuthDynamicField  string   `json:"incoming_auth_dynamic_field" binding:"omitempty,max=64"`
	WebhookSignatureHeader    string   `json:"webhook_signature_header" binding:"omitempty,max=128"`
	WebhookSignaturePrefix    string   `json:"webhook_signature_prefix" binding:"omitempty,max=64"`
	ForwardHeaders            []string `json:"forward_headers"`
	TimeoutMs                 int32    `json:"timeout_ms"`
	RetryCount                int32    `json:"retry_count"`
	RetryBackoffMs            int32    `json:"retry_backoff_ms"`
	ClickHouseTable           string   `json:"clickhouse_table" binding:"omitempty,max=129"`
	ClickHouseTemplateID      string   `json:"clickhouse_template_id" binding:"omitempty,uuid"`
	ClickHouseRetentionDays   int32    `json:"clickhouse_retention_days" binding:"omitempty,min=0,max=3650"`
	DLQTTLSeconds             int32    `json:"dlq_ttl_seconds" binding:"omitempty,min=60,max=2592000"`
	DLQRetryDelaySeconds      int32    `json:"dlq_retry_delay_seconds" binding:"omitempty,min=1,max=86400"`
	Comment                   string   `json:"comment" binding:"omitempty,max=2000"`
	Status                    string   `json:"status" binding:"omitempty,oneof=enabled disabled paused"`
	LogRequestBody            bool     `json:"log_request_body"`
	LogResponseBody           bool     `json:"log_response_body"`
	LogHeaders                bool     `json:"log_headers"`
	// LoggingEnabled — указатель, чтобы отличить «не прислано» (дефолт true,
	// сохраняет текущее поведение) от явного false (§22).
	LoggingEnabled     *bool `json:"logging_enabled"`
	MaxBodySizeEnabled bool  `json:"max_body_size_enabled"`
	MaxBodySize        int32 `json:"max_body_size" binding:"omitempty,min=0,max=10000000"`

	// §39: path-passthrough — приклеивать хвост входящего пути к target URL.
	PathPassthrough bool `json:"path_passthrough"`

	// §27: RabbitMQAsync. RMQPassword пустой в PUT = «оставить старый» (как
	// auth_credentials, разбирается в handler.Update). Диапазоны pull_* также
	// проверяет domain.Node.Validate и БД-constraint chk_rmq_fields.
	RMQHost         string `json:"rmq_host" binding:"omitempty,max=253"`
	RMQPort         int32  `json:"rmq_port" binding:"omitempty,min=1,max=65535"`
	RMQVHost        string `json:"rmq_vhost" binding:"omitempty,max=255"`
	RMQUser         string `json:"rmq_user" binding:"omitempty,max=255"`
	RMQPassword     string `json:"rmq_password" binding:"omitempty,max=1024"`
	RMQQueue        string `json:"rmq_queue" binding:"omitempty,max=255"`
	RMQUseTLS       bool   `json:"rmq_use_tls"`
	PullIntervalSec int32  `json:"pull_interval_sec" binding:"omitempty,min=1,max=3600"`
	PullBatchSize   int32  `json:"pull_batch_size" binding:"omitempty,min=1,max=1000"`
	PullPrefetch    int32  `json:"pull_prefetch" binding:"omitempty,min=1,max=1000"`
}

// UpdateNodeRequest — то же, но без path/root_method иногда позволяется
// менять. В v1 запрещаем менять path после создания — это упростит логи.
type UpdateNodeRequest = CreateNodeRequest

// NodeResponse — DTO ответа. Креды НЕ возвращаются.
type NodeResponse struct {
	ID                 string   `json:"id"`
	Path               string   `json:"path"`
	RootMethod         string   `json:"root_method"`
	IncomingMethod     string   `json:"incoming_method"`
	OutgoingMethod     string   `json:"outgoing_method"`
	URLMode            string   `json:"url_mode"`
	TargetURL          string   `json:"target_url"`
	URLParamName       string   `json:"url_param_name"`
	URLAllowedHosts    []string `json:"url_allowed_hosts"`
	AuthType           string   `json:"auth_type"`
	AuthCredentialsSet bool     `json:"auth_credentials_set"`
	// AuthLogin — логин basic-кредов исходящей авторизации (часть до первого
	// «:»). Логин — не секрет (в отличие от пароля, который наружу не отдаётся
	// никогда); нужен UI: prefill формы редактирования + вывод в просмотре
	// узла. Пусто для не-basic типов.
	AuthLogin              string `json:"auth_login,omitempty"`
	AuthDynamicSource      string `json:"auth_dynamic_source"`
	AuthDynamicField       string `json:"auth_dynamic_field"`
	AuthDynamicStripPrefix string `json:"auth_dynamic_strip_prefix"`
	IncomingAuthType       string `json:"incoming_auth_type"`
	IncomingAuthCredsSet   bool   `json:"incoming_auth_credentials_set"`
	// IncomingAuthLogin — логин basic-кредов входящей авторизации (см. AuthLogin).
	IncomingAuthLogin         string   `json:"incoming_auth_login,omitempty"`
	IncomingAuthDynamicSource string   `json:"incoming_auth_dynamic_source"`
	IncomingAuthDynamicField  string   `json:"incoming_auth_dynamic_field"`
	WebhookSignatureHeader    string   `json:"webhook_signature_header"`
	WebhookSignaturePrefix    string   `json:"webhook_signature_prefix"`
	ForwardHeaders            []string `json:"forward_headers"`
	TimeoutMs                 int32    `json:"timeout_ms"`
	RetryCount                int32    `json:"retry_count"`
	RetryBackoffMs            int32    `json:"retry_backoff_ms"`
	ClickHouseTable           string   `json:"clickhouse_table"`
	ClickHouseTemplateID      string   `json:"clickhouse_template_id"`
	ClickHouseRetentionDays   int32    `json:"clickhouse_retention_days"`
	DLQTTLSeconds             int32    `json:"dlq_ttl_seconds"`
	DLQRetryDelaySeconds      int32    `json:"dlq_retry_delay_seconds"`
	Comment                   string   `json:"comment"`
	Status                    string   `json:"status"`
	TeamID                    string   `json:"team_id"`
	LogRequestBody            bool     `json:"log_request_body"`
	LogResponseBody           bool     `json:"log_response_body"`
	LogHeaders                bool     `json:"log_headers"`
	LoggingEnabled            bool     `json:"logging_enabled"`
	MaxBodySizeEnabled        bool     `json:"max_body_size_enabled"`
	MaxBodySize               int32    `json:"max_body_size"`
	PathPassthrough           bool     `json:"path_passthrough"`

	// §27: RabbitMQAsync. Пароль не возвращается — только флаг RMQPasswordSet.
	// RMQStatus — runtime-health воркера (degraded/queue_depth/…), заполняется
	// только для RabbitMQAsync; nil для request/requestAsync.
	RMQHost         string     `json:"rmq_host"`
	RMQPort         int32      `json:"rmq_port"`
	RMQVHost        string     `json:"rmq_vhost"`
	RMQUser         string     `json:"rmq_user"`
	RMQPasswordSet  bool       `json:"rmq_password_set"`
	RMQQueue        string     `json:"rmq_queue"`
	RMQUseTLS       bool       `json:"rmq_use_tls"`
	PullIntervalSec int32      `json:"pull_interval_sec"`
	PullBatchSize   int32      `json:"pull_batch_size"`
	PullPrefetch    int32      `json:"pull_prefetch"`
	RMQStatus       *RMQStatus `json:"rmq_status,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// §63: логин автора создания и последнего изменения узла (для показа рядом
	// с «Создано»/«Обновлено»). Пусто у узлов до миграции 0026.
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
}

// RMQStatus — runtime-снимок Puller-воркера узла RabbitMQAsync (§27.4, §27.8).
// Источник — Puller-manager в Receiver через общий стор (Redis); поле degraded
// НЕ хранится в node.status.
type RMQStatus struct {
	Degraded        bool   `json:"degraded"`
	Reason          string `json:"reason,omitempty"`
	ConnectionState string `json:"connection_state"` // down|connecting|up
	QueueDepth      int64  `json:"queue_depth"`
	ConsumerCount   int64  `json:"consumer_count"`
	Attempts        int64  `json:"attempts"`
	Since           string `json:"since,omitempty"`
}

// rmqHealthToDTO конвертирует доменный health-снимок в DTO ответа (§27.8).
func rmqHealthToDTO(h *domain.RMQHealth) *RMQStatus {
	s := &RMQStatus{
		Degraded:        h.Degraded,
		Reason:          h.Reason,
		ConnectionState: string(h.ConnState),
		QueueDepth:      h.QueueDepth,
		ConsumerCount:   h.ConsumerCount,
		Attempts:        h.Attempts,
	}
	if !h.Since.IsZero() {
		s.Since = h.Since.Format(time.RFC3339)
	}
	return s
}

// reqToDomain превращает DTO в domain.Node.
// Если в запросе кредов нет (пустая строка), сохраняем существующие
// (для Update этот разбор делается выше — в handler).
func reqToDomain(r CreateNodeRequest) *domain.Node {
	hosts := r.URLAllowedHosts
	if hosts == nil {
		hosts = []string{}
	}
	headers := r.ForwardHeaders
	if headers == nil {
		headers = []string{}
	}
	loggingEnabled := true // дефолт §22: поле не прислано → логирование включено
	if r.LoggingEnabled != nil {
		loggingEnabled = *r.LoggingEnabled
	}
	return &domain.Node{
		Path:                      r.Path,
		RootMethod:                domain.RootMethod(r.RootMethod),
		IncomingMethod:            domain.HTTPMethod(r.IncomingMethod),
		OutgoingMethod:            domain.HTTPMethod(r.OutgoingMethod),
		URLMode:                   domain.URLMode(r.URLMode),
		TargetURL:                 r.TargetURL,
		URLParamName:              r.URLParamName,
		URLAllowedHosts:           hosts,
		AuthType:                  domain.AuthType(r.AuthType),
		AuthCredentials:           r.AuthCredentials,
		AuthDynamicSource:         domain.AuthDynSource(r.AuthDynamicSource),
		AuthDynamicField:          r.AuthDynamicField,
		AuthDynamicStripPrefix:    r.AuthDynamicStripPrefix,
		IncomingAuthType:          domain.IncomingAuthType(r.IncomingAuthType),
		IncomingAuthCredentials:   r.IncomingAuthCreds,
		IncomingAuthDynamicSource: domain.IncomingAuthSource(r.IncomingAuthDynamicSource),
		IncomingAuthDynamicField:  r.IncomingAuthDynamicField,
		WebhookSignatureHeader:    r.WebhookSignatureHeader,
		WebhookSignaturePrefix:    r.WebhookSignaturePrefix,
		ForwardHeaders:            headers,
		TimeoutMs:                 r.TimeoutMs,
		RetryCount:                r.RetryCount,
		RetryBackoffMs:            r.RetryBackoffMs,
		ClickHouseTable:           r.ClickHouseTable,
		ClickHouseTemplateID:      r.ClickHouseTemplateID,
		ClickHouseRetentionDays:   r.ClickHouseRetentionDays,
		DLQTTLSeconds:             r.DLQTTLSeconds,
		DLQRetryDelaySeconds:      r.DLQRetryDelaySeconds,
		Comment:                   r.Comment,
		Status:                    domain.NodeStatus(r.Status),
		LogRequestBody:            r.LogRequestBody,
		LogResponseBody:           r.LogResponseBody,
		LogHeaders:                r.LogHeaders,
		LoggingEnabled:            loggingEnabled,
		MaxBodySizeEnabled:        r.MaxBodySizeEnabled,
		MaxBodySize:               r.MaxBodySize,
		PathPassthrough:           r.PathPassthrough,
		RMQHost:                   r.RMQHost,
		RMQPort:                   r.RMQPort,
		RMQVHost:                  r.RMQVHost,
		RMQUser:                   r.RMQUser,
		RMQPassword:               r.RMQPassword,
		RMQQueue:                  r.RMQQueue,
		RMQUseTLS:                 r.RMQUseTLS,
		PullIntervalSec:           r.PullIntervalSec,
		PullBatchSize:             r.PullBatchSize,
		PullPrefetch:              r.PullPrefetch,
	}
}

func nodeToResponse(n *domain.Node) NodeResponse {
	return NodeResponse{
		ID:                        n.ID,
		Path:                      n.Path,
		RootMethod:                string(n.RootMethod),
		IncomingMethod:            string(n.IncomingMethod),
		OutgoingMethod:            string(n.OutgoingMethod),
		URLMode:                   string(n.URLMode),
		TargetURL:                 n.TargetURL,
		URLParamName:              n.URLParamName,
		URLAllowedHosts:           n.URLAllowedHosts,
		AuthType:                  string(n.AuthType),
		AuthCredentialsSet:        n.AuthCredentials != "",
		AuthLogin:                 basicLogin(n.AuthType == domain.AuthTypeBasic, n.AuthCredentials),
		AuthDynamicSource:         string(n.AuthDynamicSource),
		AuthDynamicField:          n.AuthDynamicField,
		AuthDynamicStripPrefix:    n.AuthDynamicStripPrefix,
		IncomingAuthType:          string(n.IncomingAuthType),
		IncomingAuthCredsSet:      n.IncomingAuthCredentials != "",
		IncomingAuthLogin:         basicLogin(n.IncomingAuthType == domain.IncomingAuthTypeBasic, n.IncomingAuthCredentials),
		IncomingAuthDynamicSource: string(n.IncomingAuthDynamicSource),
		IncomingAuthDynamicField:  n.IncomingAuthDynamicField,
		WebhookSignatureHeader:    n.WebhookSignatureHeader,
		WebhookSignaturePrefix:    n.WebhookSignaturePrefix,
		ForwardHeaders:            n.ForwardHeaders,
		TimeoutMs:                 n.TimeoutMs,
		RetryCount:                n.RetryCount,
		RetryBackoffMs:            n.RetryBackoffMs,
		ClickHouseTable:           n.ClickHouseTable,
		ClickHouseTemplateID:      n.ClickHouseTemplateID,
		ClickHouseRetentionDays:   n.ClickHouseRetentionDays,
		DLQTTLSeconds:             n.DLQTTLSeconds,
		DLQRetryDelaySeconds:      n.DLQRetryDelaySeconds,
		Comment:                   n.Comment,
		Status:                    string(n.Status),
		TeamID:                    n.TeamID,
		LogRequestBody:            n.LogRequestBody,
		LogResponseBody:           n.LogResponseBody,
		LogHeaders:                n.LogHeaders,
		LoggingEnabled:            n.LoggingEnabled,
		MaxBodySizeEnabled:        n.MaxBodySizeEnabled,
		MaxBodySize:               n.MaxBodySize,
		PathPassthrough:           n.PathPassthrough,
		RMQHost:                   n.RMQHost,
		RMQPort:                   n.RMQPort,
		RMQVHost:                  n.RMQVHost,
		RMQUser:                   n.RMQUser,
		RMQPasswordSet:            n.RMQPassword != "",
		RMQQueue:                  n.RMQQueue,
		RMQUseTLS:                 n.RMQUseTLS,
		PullIntervalSec:           n.PullIntervalSec,
		PullBatchSize:             n.PullBatchSize,
		PullPrefetch:              n.PullPrefetch,
		CreatedAt:                 n.CreatedAt,
		UpdatedAt:                 n.UpdatedAt,
		CreatedBy:                 n.CreatedBy,
		UpdatedBy:                 n.UpdatedBy,
	}
}
