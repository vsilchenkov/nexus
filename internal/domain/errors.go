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
	ErrNodeNotFound                = errors.New("domain: node not found")
	ErrNodeAlreadyExists           = errors.New("domain: node with this path already exists")
	ErrNodeInvalidRootMethod       = errors.New("domain: invalid root_method")
	ErrNodeInvalidURLMode          = errors.New("domain: invalid url_mode")
	ErrNodeInvalidAuthType         = errors.New("domain: invalid auth_type")
	ErrNodeInvalidIncomingAuthType = errors.New("domain: invalid incoming_auth_type")
	ErrNodeInvalidAuthDynSource    = errors.New("domain: invalid auth_dynamic_source")
	ErrNodeInvalidStatus           = errors.New("domain: invalid status")
	ErrNodePathLength              = errors.New("domain: path length must be 1..255")
	ErrNodePathFormat              = errors.New("domain: path must match ^[a-zA-Z0-9][a-zA-Z0-9/_-]*$")
	ErrNodeTargetURLLength         = errors.New("domain: target_url length must be <= 2048")
	ErrNodeStaticNeedsTargetURL    = errors.New("domain: url_mode=static requires non-empty target_url")
	ErrNodeParamNameLength         = errors.New("domain: url_param_name length must be 1..64")
	ErrNodeParamNameFormat         = errors.New("domain: url_param_name must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeAuthDynFieldLength      = errors.New("domain: auth_dynamic_field length must be 1..64")
	ErrNodeAuthDynFieldFormat      = errors.New("domain: auth_dynamic_field must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeTimeoutRange            = errors.New("domain: timeout_ms must be 100..300000")
	ErrNodeRetryCountRange         = errors.New("domain: retry_count must be 0..10")
	ErrNodeRetryBackoffRange       = errors.New("domain: retry_backoff_ms must be 0..60000")
	ErrNodeAllowedHostsSize        = errors.New("domain: url_allowed_hosts must have at most 50 elements")
	ErrNodeForwardHeadersSize      = errors.New("domain: forward_headers must have at most 30 elements")
	ErrNodeDisabled                = errors.New("domain: node disabled")
	ErrNodePaused                  = errors.New("domain: node paused")
	ErrNodeInvalidTemplateID       = errors.New("domain: clickhouse_template_id must be a valid UUID")
	ErrNodeLogsNotConfigured       = errors.New("domain: node has no clickhouse_table configured")
	// Webhook signature (§16 ТЗ, IncomingAuthTypeWebhookSignature).
	ErrNodeWebhookSigHeaderLength   = errors.New("domain: webhook_signature_header length must be <= 128")
	ErrNodeWebhookSigPrefixLength   = errors.New("domain: webhook_signature_prefix length must be <= 64")
	ErrNodeWebhookSigHeaderRequired = errors.New("domain: webhook_signature_header is required for incoming_auth_type=webhook_signature")
	ErrNodeWebhookSigSecretRequired = errors.New("domain: incoming_auth_credentials (secret) is required for incoming_auth_type=webhook_signature")
	ErrCallbackNotAllowed           = errors.New("domain: /v1/callback route requires incoming_auth_type=webhook_signature")

	// §22: контроль логирования узла.
	ErrNodeMaxBodySizeRange    = errors.New("domain: max_body_size must be 0..10000000")
	ErrNodeMaxBodySizeRequired = errors.New("domain: max_body_size must be > 0 when max_body_size_enabled")

	// User / Session
	ErrUserNotFound      = errors.New("domain: user not found")
	ErrUserAlreadyExists = errors.New("domain: user with this login already exists")
	ErrUserInactive      = errors.New("domain: user inactive")
	ErrSessionNotFound   = errors.New("domain: session not found")
	ErrSessionExpired    = errors.New("domain: session expired")

	// Auth (входящий запрос)
	ErrUnauthorized        = errors.New("domain: unauthorized")
	ErrAuthHeaderMissing   = errors.New("domain: authorization header missing")
	ErrAuthHeaderMalformed = errors.New("domain: authorization header malformed")
	ErrAuthTokenRequired   = errors.New("domain: auth token required in incoming request")

	// URL resolution
	ErrURLParamRequired = errors.New("domain: url parameter is required")
	ErrURLInvalid       = errors.New("domain: url is invalid")
	ErrURLNotAllowed    = errors.New("domain: url is not in allowlist")

	// CH-шаблоны (§19)
	ErrCHTemplateNotFound                = errors.New("domain: clickhouse template not found")
	ErrCHTemplateAlreadyExists           = errors.New("domain: clickhouse template with this name already exists")
	ErrCHTemplateNameFormat              = errors.New("domain: template name must match ^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$")
	ErrCHTemplateDescriptionLength       = errors.New("domain: template description length must be <= 1000")
	ErrCHTemplateInvalidEngine           = errors.New("domain: template engine must be MergeTree")
	ErrCHTemplateInvalidPartition        = errors.New("domain: template partition_by is not in the allowed set")
	ErrCHTemplateEmptyOrderBy            = errors.New("domain: template order_by must be non-empty")
	ErrCHTemplateInvalidOrderBy          = errors.New("domain: template order_by must reference required log columns")
	ErrCHTemplateUnknownColumn           = errors.New("domain: template column override references an unknown column")
	ErrCHTemplateInvalidCodec            = errors.New("domain: template column codec is invalid")
	ErrCHTemplateInvalidIndexName        = errors.New("domain: template index name must match ^[A-Za-z0-9_]+$")
	ErrCHTemplateInvalidIndexExpr        = errors.New("domain: template index expr must reference a required log column")
	ErrCHTemplateInvalidIndexType        = errors.New("domain: template index type is invalid")
	ErrCHTemplateInvalidIndexGranularity = errors.New("domain: template index granularity must be 1..1000000")
	ErrCHTemplateInvalidTTLMode          = errors.New("domain: template ttl_mode must be none or ttl_days")
	ErrCHTemplateTTLDaysRequired         = errors.New("domain: ttl_days must be > 0 when ttl_mode=ttl_days")
	ErrCHTemplateInvalidTableName        = errors.New("domain: table name must match db.table")
	ErrCHTemplateInUse                   = errors.New("domain: template is used by nodes and cannot be deleted")
	ErrCHTemplateDefaultImmutable        = errors.New("domain: default template cannot be deleted")

	// Host allowlist catalog (§23)
	ErrHostNotFound          = errors.New("domain: host pattern not found")
	ErrHostAlreadyExists     = errors.New("domain: host pattern already exists")
	ErrHostInvalidKind       = errors.New("domain: host kind must be exact, wildcard or regex")
	ErrHostPatternLength     = errors.New("domain: host pattern length must be 1..512")
	ErrHostExactFormat       = errors.New("domain: exact host must be a valid hostname")
	ErrHostWildcardFormat    = errors.New("domain: wildcard host must be *.<hostname>")
	ErrHostRegexInvalid      = errors.New("domain: host regex does not compile")
	ErrHostDescriptionLength = errors.New("domain: host description length must be <= 500")
	ErrHostInUse             = errors.New("domain: host pattern is used by nodes and cannot be modified or deleted")

	// Headers catalog (§24)
	ErrHeaderNotFound          = errors.New("domain: header not found")
	ErrHeaderAlreadyExists     = errors.New("domain: header with this name already exists")
	ErrHeaderNameLength        = errors.New("domain: header name length must be 1..100")
	ErrHeaderNameFormat        = errors.New("domain: header name must be a valid RFC 7230 token")
	ErrHeaderDescriptionLength = errors.New("domain: header description length must be <= 500")

	// Уведомления (§20)
	ErrTelegramCronInvalid = errors.New("domain: invalid telegram cron expression")

	// Team (multi-tenancy v2)
	ErrTeamNotFound         = errors.New("domain: team not found")
	ErrTeamAlreadyExists    = errors.New("domain: team with this slug or ch_database already exists")
	ErrTeamSlugFormat       = errors.New("domain: team slug must match ^[a-z][a-z0-9_]{0,31}$")
	ErrTeamNameLength       = errors.New("domain: team name length must be 1..255")
	ErrTeamCHDatabaseFormat = errors.New("domain: team ch_database must match ^nexus_[a-z][a-z0-9_]{0,31}$")
	ErrTeamInvalidRole      = errors.New("domain: invalid team role")
	ErrTeamMemberNotFound   = errors.New("domain: team membership not found")
)
