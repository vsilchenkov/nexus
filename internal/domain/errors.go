package domain

import "errors"

// Sentinel-ошибки доменного слоя. Handler-ы (HTTP/gRPC) маппят их
// в коды через errors.Is.
var (
	// Общие
	ErrNotFound         = errors.New("domain: not found")
	ErrAlreadyExists    = errors.New("domain: already exists")
	ErrPermissionDenied = errors.New("domain: permission denied")
	ErrLimitReached     = errors.New("domain: limit reached")

	// Node
	ErrNodeNotFound              = errors.New("domain: node not found")
	ErrNodeAlreadyExists         = errors.New("domain: node with this path already exists")
	ErrNodeInvalidRootMethod     = errors.New("domain: invalid root_method")
	ErrNodeInvalidURLMode        = errors.New("domain: invalid url_mode")
	ErrNodeInvalidAuthType       = errors.New("domain: invalid auth_type")
	ErrNodeInvalidIncomingAuthType = errors.New("domain: invalid incoming_auth_type")
	ErrNodeInvalidAuthDynSource  = errors.New("domain: invalid auth_dynamic_source")
	ErrNodeInvalidStatus         = errors.New("domain: invalid status")
	ErrNodePathLength            = errors.New("domain: path length must be 1..255")
	ErrNodePathFormat            = errors.New("domain: path must match ^[a-zA-Z0-9][a-zA-Z0-9/_-]*$")
	ErrNodeTargetURLLength       = errors.New("domain: target_url length must be <= 2048")
	ErrNodeStaticNeedsTargetURL  = errors.New("domain: url_mode=static requires non-empty target_url")
	ErrNodeParamNameLength       = errors.New("domain: url_param_name length must be 1..64")
	ErrNodeParamNameFormat       = errors.New("domain: url_param_name must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeAuthDynFieldLength    = errors.New("domain: auth_dynamic_field length must be 1..64")
	ErrNodeAuthDynFieldFormat    = errors.New("domain: auth_dynamic_field must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeTimeoutRange          = errors.New("domain: timeout_ms must be 100..300000")
	ErrNodeRetryCountRange       = errors.New("domain: retry_count must be 0..10")
	ErrNodeRetryBackoffRange     = errors.New("domain: retry_backoff_ms must be 0..60000")
	ErrNodeAllowedHostsSize      = errors.New("domain: url_allowed_hosts must have at most 50 elements")
	ErrNodeForwardHeadersSize    = errors.New("domain: forward_headers must have at most 30 elements")
	ErrNodeDisabled              = errors.New("domain: node disabled")
	ErrNodePaused                = errors.New("domain: node paused")
	// Webhook signature (§16 ТЗ, IncomingAuthTypeWebhookSignature).
	ErrNodeWebhookSigHeaderLength   = errors.New("domain: webhook_signature_header length must be <= 128")
	ErrNodeWebhookSigPrefixLength   = errors.New("domain: webhook_signature_prefix length must be <= 64")
	ErrNodeWebhookSigHeaderRequired = errors.New("domain: webhook_signature_header is required for incoming_auth_type=webhook_signature")
	ErrNodeWebhookSigSecretRequired = errors.New("domain: incoming_auth_credentials (secret) is required for incoming_auth_type=webhook_signature")
	ErrCallbackNotAllowed           = errors.New("domain: /v1/callback route requires incoming_auth_type=webhook_signature")

	// User / Session
	ErrUserNotFound      = errors.New("domain: user not found")
	ErrUserAlreadyExists = errors.New("domain: user with this login already exists")
	ErrUserInactive      = errors.New("domain: user inactive")
	ErrSessionNotFound   = errors.New("domain: session not found")
	ErrSessionExpired    = errors.New("domain: session expired")

	// Auth (входящий запрос)
	ErrUnauthorized         = errors.New("domain: unauthorized")
	ErrAuthHeaderMissing    = errors.New("domain: authorization header missing")
	ErrAuthHeaderMalformed  = errors.New("domain: authorization header malformed")
	ErrAuthTokenRequired    = errors.New("domain: auth token required in incoming request")

	// URL resolution
	ErrURLParamRequired = errors.New("domain: url parameter is required")
	ErrURLInvalid       = errors.New("domain: url is invalid")
	ErrURLNotAllowed    = errors.New("domain: url is not in allowlist")
)
