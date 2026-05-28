// Package http — Gin handlers и DTO Web Service.
package http

import (
	"time"

	"nexus/internal/domain"
)

// CredentialsMask — что возвращаем вместо реального значения кредов.
// §5.5 ТЗ: «В UI значения никогда не возвращаются в API-ответах в открытом виде».
const CredentialsMask = "***"

// CreateNodeRequest — DTO для POST /api/nodes.
type CreateNodeRequest struct {
	Path                    string   `json:"path" binding:"required,max=255"`
	RootMethod              string   `json:"root_method" binding:"required,oneof=request requestAsync"`
	URLMode                 string   `json:"url_mode" binding:"omitempty,oneof=static from_request"`
	TargetURL               string   `json:"target_url" binding:"omitempty,max=2048"`
	URLParamName            string   `json:"url_param_name" binding:"omitempty,max=64"`
	URLAllowedHosts         []string `json:"url_allowed_hosts"`
	AuthType                string   `json:"auth_type" binding:"omitempty,oneof=none basic token token_from_request basic_from_request"`
	AuthCredentials         string   `json:"auth_credentials" binding:"omitempty,max=1024"`
	AuthDynamicSource       string   `json:"auth_dynamic_source" binding:"omitempty,oneof=query header body"`
	AuthDynamicField        string   `json:"auth_dynamic_field" binding:"omitempty,max=64"`
	AuthDynamicStripPrefix  string   `json:"auth_dynamic_strip_prefix" binding:"omitempty,max=64"`
	IncomingAuthType        string   `json:"incoming_auth_type" binding:"omitempty,oneof=none basic token webhook_signature"`
	IncomingAuthCreds       string   `json:"incoming_auth_credentials" binding:"omitempty,max=1024"`
	WebhookSignatureHeader  string   `json:"webhook_signature_header" binding:"omitempty,max=128"`
	WebhookSignaturePrefix  string   `json:"webhook_signature_prefix" binding:"omitempty,max=64"`
	ForwardHeaders          []string `json:"forward_headers"`
	TimeoutMs               int32    `json:"timeout_ms"`
	RetryCount              int32    `json:"retry_count"`
	RetryBackoffMs          int32    `json:"retry_backoff_ms"`
	ClickHouseTable         string   `json:"clickhouse_table" binding:"omitempty,max=129"`
	ClickHouseTemplateID    string   `json:"clickhouse_template_id" binding:"omitempty,uuid"`
	ClickHouseRetentionDays int32    `json:"clickhouse_retention_days" binding:"omitempty,min=0,max=3650"`
	Status                  string   `json:"status" binding:"omitempty,oneof=enabled disabled paused"`
	LogRequestBody          bool     `json:"log_request_body"`
	LogResponseBody         bool     `json:"log_response_body"`
	LogHeaders              bool     `json:"log_headers"`
}

// UpdateNodeRequest — то же, но без path/root_method иногда позволяется
// менять. В v1 запрещаем менять path после создания — это упростит логи.
type UpdateNodeRequest = CreateNodeRequest

// NodeResponse — DTO ответа. Креды НЕ возвращаются.
type NodeResponse struct {
	ID                      string    `json:"id"`
	Path                    string    `json:"path"`
	RootMethod              string    `json:"root_method"`
	URLMode                 string    `json:"url_mode"`
	TargetURL               string    `json:"target_url"`
	URLParamName            string    `json:"url_param_name"`
	URLAllowedHosts         []string  `json:"url_allowed_hosts"`
	AuthType                string    `json:"auth_type"`
	AuthCredentialsSet      bool      `json:"auth_credentials_set"`
	AuthDynamicSource       string    `json:"auth_dynamic_source"`
	AuthDynamicField        string    `json:"auth_dynamic_field"`
	AuthDynamicStripPrefix  string    `json:"auth_dynamic_strip_prefix"`
	IncomingAuthType        string    `json:"incoming_auth_type"`
	IncomingAuthCredsSet    bool      `json:"incoming_auth_credentials_set"`
	WebhookSignatureHeader  string    `json:"webhook_signature_header"`
	WebhookSignaturePrefix  string    `json:"webhook_signature_prefix"`
	ForwardHeaders          []string  `json:"forward_headers"`
	TimeoutMs               int32     `json:"timeout_ms"`
	RetryCount              int32     `json:"retry_count"`
	RetryBackoffMs          int32     `json:"retry_backoff_ms"`
	ClickHouseTable         string    `json:"clickhouse_table"`
	ClickHouseTemplateID    string    `json:"clickhouse_template_id"`
	ClickHouseRetentionDays int32     `json:"clickhouse_retention_days"`
	Status                  string    `json:"status"`
	TeamID                  string    `json:"team_id"`
	LogRequestBody          bool      `json:"log_request_body"`
	LogResponseBody         bool      `json:"log_response_body"`
	LogHeaders              bool      `json:"log_headers"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
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
	return &domain.Node{
		Path:                    r.Path,
		RootMethod:              domain.RootMethod(r.RootMethod),
		URLMode:                 domain.URLMode(r.URLMode),
		TargetURL:               r.TargetURL,
		URLParamName:            r.URLParamName,
		URLAllowedHosts:         hosts,
		AuthType:                domain.AuthType(r.AuthType),
		AuthCredentials:         r.AuthCredentials,
		AuthDynamicSource:       domain.AuthDynSource(r.AuthDynamicSource),
		AuthDynamicField:        r.AuthDynamicField,
		AuthDynamicStripPrefix:  r.AuthDynamicStripPrefix,
		IncomingAuthType:        domain.IncomingAuthType(r.IncomingAuthType),
		IncomingAuthCredentials: r.IncomingAuthCreds,
		WebhookSignatureHeader:  r.WebhookSignatureHeader,
		WebhookSignaturePrefix:  r.WebhookSignaturePrefix,
		ForwardHeaders:          headers,
		TimeoutMs:               r.TimeoutMs,
		RetryCount:              r.RetryCount,
		RetryBackoffMs:          r.RetryBackoffMs,
		ClickHouseTable:         r.ClickHouseTable,
		ClickHouseTemplateID:    r.ClickHouseTemplateID,
		ClickHouseRetentionDays: r.ClickHouseRetentionDays,
		Status:                  domain.NodeStatus(r.Status),
		LogRequestBody:          r.LogRequestBody,
		LogResponseBody:         r.LogResponseBody,
		LogHeaders:              r.LogHeaders,
	}
}

func nodeToResponse(n *domain.Node) NodeResponse {
	return NodeResponse{
		ID:                      n.ID,
		Path:                    n.Path,
		RootMethod:              string(n.RootMethod),
		URLMode:                 string(n.URLMode),
		TargetURL:               n.TargetURL,
		URLParamName:            n.URLParamName,
		URLAllowedHosts:         n.URLAllowedHosts,
		AuthType:                string(n.AuthType),
		AuthCredentialsSet:      n.AuthCredentials != "",
		AuthDynamicSource:       string(n.AuthDynamicSource),
		AuthDynamicField:        n.AuthDynamicField,
		AuthDynamicStripPrefix:  n.AuthDynamicStripPrefix,
		IncomingAuthType:        string(n.IncomingAuthType),
		IncomingAuthCredsSet:    n.IncomingAuthCredentials != "",
		WebhookSignatureHeader:  n.WebhookSignatureHeader,
		WebhookSignaturePrefix:  n.WebhookSignaturePrefix,
		ForwardHeaders:          n.ForwardHeaders,
		TimeoutMs:               n.TimeoutMs,
		RetryCount:              n.RetryCount,
		RetryBackoffMs:          n.RetryBackoffMs,
		ClickHouseTable:         n.ClickHouseTable,
		ClickHouseTemplateID:    n.ClickHouseTemplateID,
		ClickHouseRetentionDays: n.ClickHouseRetentionDays,
		Status:                  string(n.Status),
		TeamID:                  n.TeamID,
		LogRequestBody:          n.LogRequestBody,
		LogResponseBody:         n.LogResponseBody,
		LogHeaders:              n.LogHeaders,
		CreatedAt:               n.CreatedAt,
		UpdatedAt:               n.UpdatedAt,
	}
}
