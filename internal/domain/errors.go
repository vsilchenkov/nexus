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
	ErrNodeInvalidIncomingMethod   = errors.New("domain: invalid incoming_method (allowed: GET, POST, PUT, DELETE)")
	ErrNodeInvalidOutgoingMethod   = errors.New("domain: invalid outgoing_method (allowed: GET, POST, PUT, DELETE)")
	ErrNodeInvalidURLMode          = errors.New("domain: invalid url_mode")
	ErrNodeInvalidAuthType         = errors.New("domain: invalid auth_type")
	ErrNodeInvalidIncomingAuthType = errors.New("domain: invalid incoming_auth_type")
	ErrNodeInvalidAuthDynSource    = errors.New("domain: invalid auth_dynamic_source")
	ErrNodeInvalidStatus           = errors.New("domain: invalid status")
	ErrNodePathLength              = errors.New("domain: path length must be 1..255")
	ErrNodePathFormat              = errors.New("domain: path must match ^[a-zA-Z0-9][a-zA-Z0-9/_-]*$")
	ErrNodeTargetURLLength         = errors.New("domain: target_url length must be <= 2048")
	ErrNodeStaticNeedsTargetURL    = errors.New("domain: url_mode=static requires non-empty target_url")
	// §69.2: target_url без схемы (http/https) валиден на всех уровнях, но падает
	// в Sender'е как `unsupported protocol scheme ""` — уже на доставке, а не на
	// сохранении узла.
	ErrNodeTargetURLScheme = errors.New("domain: target_url must be an absolute http(s) URL")
	// §32: target_url указывает на собственный ingress Nexus (петля).
	ErrNodeTargetURLSelfReference = errors.New("domain: target_url must not point at the Nexus ingress itself (loop)")
	ErrNodeParamNameLength        = errors.New("domain: url_param_name length must be 1..64")
	ErrNodeParamNameFormat        = errors.New("domain: url_param_name must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeAuthDynFieldLength     = errors.New("domain: auth_dynamic_field length must be 1..64")
	ErrNodeAuthDynFieldFormat     = errors.New("domain: auth_dynamic_field must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	// §41: входящая динамическая авторизация (incoming_auth_dynamic_source/field).
	ErrNodeInvalidIncomingAuthDynSource = errors.New("domain: invalid incoming_auth_dynamic_source (allowed: header, query)")
	ErrNodeIncomingAuthDynFieldLength   = errors.New("domain: incoming_auth_dynamic_field length must be 1..64")
	ErrNodeIncomingAuthDynFieldFormat   = errors.New("domain: incoming_auth_dynamic_field must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrNodeTimeoutRange                 = errors.New("domain: timeout_ms must be 100..600000")
	ErrNodeRetryCountRange              = errors.New("domain: retry_count must be 0..10")
	ErrNodeRetryBackoffRange            = errors.New("domain: retry_backoff_ms must be 0..60000")
	ErrNodeDLQTTLRange                  = errors.New("domain: dlq_ttl_seconds must be 60..2592000")
	ErrNodeDLQRetryDelayRange           = errors.New("domain: dlq_retry_delay_seconds must be 1..86400")
	ErrNodeAllowedHostsSize             = errors.New("domain: url_allowed_hosts must have at most 50 elements")
	ErrNodeForwardHeadersSize           = errors.New("domain: forward_headers must have at most 30 elements")
	ErrNodeDisabled                     = errors.New("domain: node disabled")
	ErrNodePaused                       = errors.New("domain: node paused")
	ErrNodeMethodNotAllowed             = errors.New("domain: http method not allowed for this node")
	ErrNodeInvalidTemplateID            = errors.New("domain: clickhouse_template_id must be a valid UUID")
	ErrNodeLogsNotConfigured            = errors.New("domain: node has no clickhouse_table configured")
	// Webhook signature (§16 ТЗ, IncomingAuthTypeWebhookSignature).
	ErrNodeWebhookSigHeaderLength   = errors.New("domain: webhook_signature_header length must be <= 128")
	ErrNodeWebhookSigPrefixLength   = errors.New("domain: webhook_signature_prefix length must be <= 64")
	ErrNodeWebhookSigHeaderRequired = errors.New("domain: webhook_signature_header is required for incoming_auth_type=webhook_signature")
	ErrNodeWebhookSigSecretRequired = errors.New("domain: incoming_auth_credentials (secret) is required for incoming_auth_type=webhook_signature")
	ErrCallbackNotAllowed           = errors.New("domain: /api/v1/callback route requires incoming_auth_type=webhook_signature")

	// §22: контроль логирования узла.
	ErrNodeMaxBodySizeRange    = errors.New("domain: max_body_size must be 0..10000000")
	ErrNodeMaxBodySizeRequired = errors.New("domain: max_body_size must be > 0 when max_body_size_enabled")

	// §42: формат имени ClickHouse-таблицы — строго db.table из [A-Za-z0-9_].
	ErrNodeClickHouseTableInvalid = errors.New("domain: clickhouse_table must be db.table of [A-Za-z0-9_]")

	// §64: внешняя (ручная) таблица логов.
	// ErrNodeExternalTableTemplateConflict — валидация узла: external_table
	// несовместим с выбранным CH-шаблоном.
	// ErrNodeExternalTable — операция управления таблицей (schema-sync) запрошена
	// для узла с внешней таблицей; такие таблицы Nexus не изменяет.
	ErrNodeExternalTableTemplateConflict = errors.New("domain: external_table is incompatible with clickhouse_template_id")
	ErrNodeExternalTable                 = errors.New("domain: node uses an external clickhouse table")

	// §29: комментарий узла.
	ErrNodeCommentLength = errors.New("domain: comment length must be <= 2000")

	// §27: узел RabbitMQAsync.
	ErrNodeRMQHostRequired   = errors.New("domain: rmq_host length must be 1..253 for RabbitMQAsync")
	ErrNodeRMQQueueInvalid   = errors.New("domain: rmq_queue length must be 1..255 and match ^[a-zA-Z0-9._-]+$")
	ErrNodePullIntervalRange = errors.New("domain: pull_interval_sec must be 1..3600")
	ErrNodePullBatchRange    = errors.New("domain: pull_batch_size must be 1..1000")
	ErrNodePullPrefetchRange = errors.New("domain: pull_prefetch must be 1..1000")

	// Чтение логов/метрик из ClickHouse (read-path).
	// ErrLogsBackendUnavailable — backend логов (ClickHouse) временно недоступен
	// (сеть/таймаут/сервер лежит), в отличие от серверной ошибки запроса
	// (битый SQL, нет таблицы). Read-эндпоинты деградируют мягко: 200 с пустыми
	// данными + флаг logs_available=false и WARN-лог (не ERROR) — чтобы поллинг
	// UI не сыпал 500 и не флудил Sentry, скрывая реальные write-path ошибки CH.
	ErrLogsBackendUnavailable = errors.New("domain: logs backend (clickhouse) temporarily unavailable")

	// User / Session
	ErrUserNotFound      = errors.New("domain: user not found")
	ErrUserAlreadyExists = errors.New("domain: user with this login already exists")
	ErrUserInactive      = errors.New("domain: user inactive")
	// ErrUserNotTeamMember — §45: попытка назначить пользователю дефолтную
	// команду, в которой он не состоит. Нельзя сделать дефолтной чужую команду
	// (иначе §18.9 при входе всё равно перекинет на команду по членству).
	ErrUserNotTeamMember = errors.New("domain: user is not a member of the team")
	// ErrFavoriteTeamsInvalid — §49: список избранных команд не проходит
	// валидацию (дубликаты team_id или больше лимита).
	ErrFavoriteTeamsInvalid = errors.New("domain: favorite teams list invalid")
	ErrSessionNotFound      = errors.New("domain: session not found")
	ErrSessionExpired       = errors.New("domain: session expired")

	// Персональные предпочтения (§71)
	ErrPreferenceKeyInvalid   = errors.New("domain: preference key must match ^[a-z][a-z0-9_]*(\\.[a-z0-9_]+)*$ and be at most 64 chars")
	ErrPreferenceValueInvalid = errors.New("domain: preference value must be valid non-null json of at most 4096 bytes")
	// ErrPreferencesLimit — у пользователя уже максимум записей предпочтений, и
	// запрос добавляет ЕЩЁ ОДИН ключ. Обновление существующего проходит всегда.
	ErrPreferencesLimit = errors.New("domain: preferences limit for this user exceeded")

	// Auth (входящий запрос)
	ErrUnauthorized        = errors.New("domain: unauthorized")
	ErrAuthHeaderMissing   = errors.New("domain: authorization header missing")
	ErrAuthHeaderMalformed = errors.New("domain: authorization header malformed")
	ErrAuthTokenRequired   = errors.New("domain: auth token required in incoming request")

	// URL resolution
	ErrURLParamRequired = errors.New("domain: url parameter is required")
	ErrURLInvalid       = errors.New("domain: url is invalid")
	ErrURLNotAllowed    = errors.New("domain: url is not in allowlist")

	// §32: защита от зацикливания запросов (hop-счётчик X-Nexus-Hops).
	ErrLoopDetected = errors.New("domain: request loop detected (max hops exceeded)")

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
	ErrHeaderInUse             = errors.New("domain: header is used by nodes and cannot be renamed or deleted")

	// §41: каталог полей запроса (request_fields_catalog).
	ErrRequestFieldNotFound          = errors.New("domain: request field not found")
	ErrRequestFieldAlreadyExists     = errors.New("domain: request field with this name already exists")
	ErrRequestFieldNameLength        = errors.New("domain: request field name length must be 1..64")
	ErrRequestFieldNameFormat        = errors.New("domain: request field name must match ^[a-zA-Z][a-zA-Z0-9_-]*$")
	ErrRequestFieldDescriptionLength = errors.New("domain: request field description length must be <= 500")

	// Уведомления (§20)
	ErrTelegramCronInvalid = errors.New("domain: invalid telegram cron expression")

	// Общие настройки (§28, Пункт 1)
	ErrPublicBaseURLInvalid = errors.New("domain: public_base_url must be an http(s) origin without path or trailing slash")

	// Интервал автообновления метрик (§44.C)
	ErrMetricsRefetchInvalid = errors.New("domain: metrics_refetch_ms must be within [1000, 120000]")

	// Версия и сессия (§34.2 / §34.3)
	ErrVersionOverrideForbidden = errors.New("domain: version override is not allowed (web.allow_version_override is off)")
	ErrSessionTTLInvalid        = errors.New("domain: session_ttl_seconds must be within [300, 2592000]")

	// Уровень логирования сервисов (§51)
	ErrLogLevelInvalid = errors.New("domain: logging.level must be within [2, 5] (2=error..5=debug)")

	// Консоль служебных логов (§51.5)
	ErrServiceLogInvalidService = errors.New("domain: unknown service (want receiver|sender|web|all)")
	ErrServiceLogInvalidLevel   = errors.New("domain: invalid min_level (want error|warn|info|debug)")

	// Team (multi-tenancy v2)
	ErrTeamNotFound         = errors.New("domain: team not found")
	ErrTeamAlreadyExists    = errors.New("domain: team with this slug or ch_database already exists")
	ErrTeamSlugFormat       = errors.New("domain: team slug must match ^[a-z][a-z0-9_]{0,31}$")
	ErrTeamNameLength       = errors.New("domain: team name length must be 1..255")
	ErrTeamCHDatabaseFormat = errors.New("domain: team ch_database must match ^nexus_[a-z][a-z0-9_]{0,40}$")
	// ErrTeamSlugTooLongForInstance — §70.2: слаг не помещается в имя БД этой
	// ноды. Отдельная ошибка вместо ErrTeamCHDatabaseFormat: из «ch_database must
	// match …» оператору непонятно, что чинить слаг, а не имя БД (его он не задаёт).
	ErrTeamSlugTooLongForInstance = errors.New("domain: team slug is too long for this instance id")
	ErrTeamInvalidRole            = errors.New("domain: invalid team role")
	ErrTeamMemberNotFound         = errors.New("domain: team membership not found")
	ErrTeamHasNodes               = errors.New("domain: team has attached nodes and cannot be deleted")

	// Instance (§70: несколько нод на одном ClickHouse)
	ErrInstanceIDFormat = errors.New("domain: instance id must be empty or match ^[a-z][a-z0-9]{0,7}$")
	// ErrCHForeignDatabase — операция затрагивает ClickHouse-БД, владельцем
	// которой является другая нода (§70.3/§70.4). Разрушающие операции по такой
	// БД не выполняются никогда, даже с аварийными флагами.
	ErrCHForeignDatabase = errors.New("domain: clickhouse database belongs to another nexus instance")
	// ErrNodeCHTableForeignDatabase — узел ссылается на таблицу в чужой БД
	// (§70.6). Допустимо только в режиме внешней таблицы (external_table, §64).
	ErrNodeCHTableForeignDatabase = errors.New("domain: clickhouse_table points at a database owned by another nexus instance")
)
