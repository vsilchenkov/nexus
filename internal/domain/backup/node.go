package backup

import (
	"encoding/json"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

// Node — строка nodes. Креды — открытым текстом (весь файл зашифрован
// паролем, §110.3). Nullable-колонки представлены нулевым значением, как в
// domain.Node (NULL ↔ 0/""), — репозиторий переводит их так же.
type Node struct {
	ID                        string          `json:"id"`
	Path                      string          `json:"path"`
	RootMethod                string          `json:"root_method"`
	IncomingMethod            string          `json:"incoming_method"`
	OutgoingMethod            string          `json:"outgoing_method"`
	URLMode                   string          `json:"url_mode"`
	TargetURL                 string          `json:"target_url"`
	URLParamName              string          `json:"url_param_name"`
	URLAllowedHosts           []string        `json:"url_allowed_hosts"`
	PathPassthrough           bool            `json:"path_passthrough"`
	SSEDecline                bool            `json:"sse_decline"`
	AuthType                  string          `json:"auth_type"`
	AuthCredentials           string          `json:"auth_credentials"`
	AuthDynamicSource         string          `json:"auth_dynamic_source"`
	AuthDynamicField          string          `json:"auth_dynamic_field"`
	AuthDynamicStripPrefix    string          `json:"auth_dynamic_strip_prefix"`
	IncomingAuthType          string          `json:"incoming_auth_type"`
	IncomingAuthCredentials   string          `json:"incoming_auth_credentials"`
	IncomingAuthDynamicSource string          `json:"incoming_auth_dynamic_source"`
	IncomingAuthDynamicField  string          `json:"incoming_auth_dynamic_field"`
	WebhookSignatureHeader    string          `json:"webhook_signature_header"`
	WebhookSignaturePrefix    string          `json:"webhook_signature_prefix"`
	ForwardHeaders            []string        `json:"forward_headers"`
	TimeoutMs                 int32           `json:"timeout_ms"`
	RetryCount                int32           `json:"retry_count"`
	RetryBackoffMs            int32           `json:"retry_backoff_ms"`
	ClickHouseTable           string          `json:"clickhouse_table"`
	ClickHouseTemplateID      string          `json:"clickhouse_template_id"`
	ClickHouseRetentionDays   int32           `json:"clickhouse_retention_days"`
	ExternalTable             bool            `json:"external_table"`
	DLQTTLSeconds             int32           `json:"dlq_ttl_seconds"`
	DLQRetryDelaySeconds      int32           `json:"dlq_retry_delay_seconds"`
	CircuitBreakerThreshold   int32           `json:"circuit_breaker_threshold"`
	CircuitBreakerCooldownSec int32           `json:"circuit_breaker_cooldown_sec"`
	BreakerIgnoreTimeouts     bool            `json:"breaker_ignore_timeouts"`
	Status                    string          `json:"status"`
	TeamID                    string          `json:"team_id"`
	GroupID                   string          `json:"group_id"`
	LogRequestBody            bool            `json:"log_request_body"`
	LogResponseBody           bool            `json:"log_response_body"`
	LogHeaders                bool            `json:"log_headers"`
	LoggingEnabled            bool            `json:"logging_enabled"`
	MaxBodySizeEnabled        bool            `json:"max_body_size_enabled"`
	MaxBodySize               int32           `json:"max_body_size"`
	RMQHost                   string          `json:"rmq_host"`
	RMQPort                   int32           `json:"rmq_port"`
	RMQVHost                  string          `json:"rmq_vhost"`
	RMQUser                   string          `json:"rmq_user"`
	RMQPassword               string          `json:"rmq_password"`
	RMQQueue                  string          `json:"rmq_queue"`
	RMQUseTLS                 bool            `json:"rmq_use_tls"`
	PullIntervalSec           int32           `json:"pull_interval_sec"`
	PullBatchSize             int32           `json:"pull_batch_size"`
	PullPrefetch              int32           `json:"pull_prefetch"`
	Comment                   string          `json:"comment"`
	AsyncAckSpec              json.RawMessage `json:"async_ack_spec,omitempty"`
	CreatedBy                 string          `json:"created_by"`
	UpdatedBy                 string          `json:"updated_by"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
}

// NodeFromDomain переводит узел домена в строку снимка.
func NodeFromDomain(n *domain.Node) (Node, error) {
	var ack json.RawMessage
	if n.AsyncAck != nil {
		raw, err := json.Marshal(n.AsyncAck)
		if err != nil {
			return Node{}, fmt.Errorf("backup: marshal async_ack_spec of node %s: %w", n.ID, err)
		}
		ack = raw
	}
	return Node{
		ID: n.ID, Path: n.Path, RootMethod: string(n.RootMethod),
		IncomingMethod: string(n.IncomingMethod), OutgoingMethod: string(n.OutgoingMethod),
		URLMode: string(n.URLMode), TargetURL: n.TargetURL, URLParamName: n.URLParamName,
		URLAllowedHosts: n.URLAllowedHosts, PathPassthrough: n.PathPassthrough, SSEDecline: n.SSEDecline,
		AuthType: string(n.AuthType), AuthCredentials: n.AuthCredentials,
		AuthDynamicSource: string(n.AuthDynamicSource), AuthDynamicField: n.AuthDynamicField,
		AuthDynamicStripPrefix: n.AuthDynamicStripPrefix,
		IncomingAuthType:       string(n.IncomingAuthType), IncomingAuthCredentials: n.IncomingAuthCredentials,
		IncomingAuthDynamicSource: string(n.IncomingAuthDynamicSource),
		IncomingAuthDynamicField:  n.IncomingAuthDynamicField,
		WebhookSignatureHeader:    n.WebhookSignatureHeader, WebhookSignaturePrefix: n.WebhookSignaturePrefix,
		ForwardHeaders: n.ForwardHeaders, TimeoutMs: n.TimeoutMs, RetryCount: n.RetryCount,
		RetryBackoffMs: n.RetryBackoffMs, ClickHouseTable: n.ClickHouseTable,
		ClickHouseTemplateID: n.ClickHouseTemplateID, ClickHouseRetentionDays: n.ClickHouseRetentionDays,
		ExternalTable: n.ExternalTable, DLQTTLSeconds: n.DLQTTLSeconds, DLQRetryDelaySeconds: n.DLQRetryDelaySeconds,
		CircuitBreakerThreshold: n.CircuitBreakerThreshold, CircuitBreakerCooldownSec: n.CircuitBreakerCooldownSec,
		BreakerIgnoreTimeouts: n.BreakerIgnoreTimeouts, Status: string(n.Status), TeamID: n.TeamID,
		GroupID: n.GroupID, LogRequestBody: n.LogRequestBody, LogResponseBody: n.LogResponseBody,
		LogHeaders: n.LogHeaders, LoggingEnabled: n.LoggingEnabled, MaxBodySizeEnabled: n.MaxBodySizeEnabled,
		MaxBodySize: n.MaxBodySize, RMQHost: n.RMQHost, RMQPort: n.RMQPort, RMQVHost: n.RMQVHost,
		RMQUser: n.RMQUser, RMQPassword: n.RMQPassword, RMQQueue: n.RMQQueue, RMQUseTLS: n.RMQUseTLS,
		PullIntervalSec: n.PullIntervalSec, PullBatchSize: n.PullBatchSize, PullPrefetch: n.PullPrefetch,
		Comment: n.Comment, AsyncAckSpec: ack, CreatedBy: n.CreatedBy, UpdatedBy: n.UpdatedBy,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}, nil
}

// ToDomain переводит строку снимка в узел домена. AsyncAck остаётся nil —
// спеку разбирает AckSpec: битая спека одного узла не должна ронять импорт
// остальных, решение о ней принимает вызывающий.
func (b Node) ToDomain() *domain.Node {
	return &domain.Node{
		ID: b.ID, Path: b.Path, RootMethod: domain.RootMethod(b.RootMethod),
		IncomingMethod: domain.HTTPMethod(b.IncomingMethod), OutgoingMethod: domain.HTTPMethod(b.OutgoingMethod),
		URLMode: domain.URLMode(b.URLMode), TargetURL: b.TargetURL, URLParamName: b.URLParamName,
		URLAllowedHosts: nonNil(b.URLAllowedHosts), PathPassthrough: b.PathPassthrough, SSEDecline: b.SSEDecline,
		AuthType: domain.AuthType(b.AuthType), AuthCredentials: b.AuthCredentials,
		AuthDynamicSource: domain.AuthDynSource(b.AuthDynamicSource), AuthDynamicField: b.AuthDynamicField,
		AuthDynamicStripPrefix:    b.AuthDynamicStripPrefix,
		IncomingAuthType:          domain.IncomingAuthType(b.IncomingAuthType),
		IncomingAuthCredentials:   b.IncomingAuthCredentials,
		IncomingAuthDynamicSource: domain.IncomingAuthSource(b.IncomingAuthDynamicSource),
		IncomingAuthDynamicField:  b.IncomingAuthDynamicField,
		WebhookSignatureHeader:    b.WebhookSignatureHeader, WebhookSignaturePrefix: b.WebhookSignaturePrefix,
		ForwardHeaders: nonNil(b.ForwardHeaders), TimeoutMs: b.TimeoutMs, RetryCount: b.RetryCount,
		RetryBackoffMs: b.RetryBackoffMs, ClickHouseTable: b.ClickHouseTable,
		ClickHouseTemplateID: b.ClickHouseTemplateID, ClickHouseRetentionDays: b.ClickHouseRetentionDays,
		ExternalTable: b.ExternalTable, DLQTTLSeconds: b.DLQTTLSeconds, DLQRetryDelaySeconds: b.DLQRetryDelaySeconds,
		CircuitBreakerThreshold: b.CircuitBreakerThreshold, CircuitBreakerCooldownSec: b.CircuitBreakerCooldownSec,
		BreakerIgnoreTimeouts: b.BreakerIgnoreTimeouts, Status: domain.NodeStatus(b.Status), TeamID: b.TeamID,
		GroupID: b.GroupID, LogRequestBody: b.LogRequestBody, LogResponseBody: b.LogResponseBody,
		LogHeaders: b.LogHeaders, LoggingEnabled: b.LoggingEnabled, MaxBodySizeEnabled: b.MaxBodySizeEnabled,
		MaxBodySize: b.MaxBodySize, RMQHost: b.RMQHost, RMQPort: b.RMQPort, RMQVHost: b.RMQVHost,
		RMQUser: b.RMQUser, RMQPassword: b.RMQPassword, RMQQueue: b.RMQQueue, RMQUseTLS: b.RMQUseTLS,
		PullIntervalSec: b.PullIntervalSec, PullBatchSize: b.PullBatchSize, PullPrefetch: b.PullPrefetch,
		Comment: b.Comment, CreatedBy: b.CreatedBy, UpdatedBy: b.UpdatedBy,
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
}

// AckSpec разбирает шаблон ответа приёма (§83). Пусто/null — nil без ошибки.
func (b Node) AckSpec() (*ackspec.Spec, error) {
	if len(b.AsyncAckSpec) == 0 || string(b.AsyncAckSpec) == "null" {
		return nil, nil
	}
	var s ackspec.Spec
	if err := json.Unmarshal(b.AsyncAckSpec, &s); err != nil {
		return nil, fmt.Errorf("backup: async_ack_spec of node %s: %w", b.ID, err)
	}
	return &s, nil
}

// nonNil — пустой срез вместо nil: domain.Node и колонки TEXT[] NOT NULL
// работают с пустым списком, а старый файл мог не нести поле вовсе.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
