package http

import (
	"errors"

	"nexus/internal/domain"
)

// nodeFieldError связывает доменную ошибку валидации узла с i18n-кодом и
// именем поля формы (для inline-подсветки на фронте, §28 Пункт 5).
type nodeFieldError struct {
	err   error
	code  string // i18n-ключ (node.validation.*)
	field string // имя поля в форме NodeSettings (для inline-вывода)
}

// nodeValidationErrors — карта всех доменных ошибок Validate()/validateRMQ()
// в (code, field). Порядок не важен — поиск по errors.Is.
var nodeValidationErrors = []nodeFieldError{
	{domain.ErrNodeInvalidRootMethod, "node.validation.root_method", "root_method"},
	{domain.ErrNodeInvalidIncomingMethod, "node.validation.incoming_method", "incoming_method"},
	{domain.ErrNodeInvalidOutgoingMethod, "node.validation.outgoing_method", "outgoing_method"},
	{domain.ErrNodeInvalidURLMode, "node.validation.url_mode", "url_mode"},
	{domain.ErrNodeInvalidAuthType, "node.validation.auth_type", "auth_type"},
	{domain.ErrNodeInvalidIncomingAuthType, "node.validation.incoming_auth_type", "incoming_auth_type"},
	{domain.ErrNodeInvalidStatus, "node.validation.status", "status"},
	{domain.ErrNodePathLength, "node.validation.path_length", "path"},
	{domain.ErrNodePathFormat, "node.validation.path_format", "path"},
	{domain.ErrNodeStaticNeedsTargetURL, "node.validation.target_url_required", "target_url"},
	{domain.ErrNodeTargetURLLength, "node.validation.target_url_length", "target_url"},
	{domain.ErrNodeTargetURLScheme, "node.validation.target_url_scheme", "target_url"},
	{domain.ErrNodeTargetURLSelfReference, "node.validation.target_url_self", "target_url"},
	{domain.ErrNodeParamNameLength, "node.validation.url_param_name_length", "url_param_name"},
	{domain.ErrNodeParamNameFormat, "node.validation.url_param_name_format", "url_param_name"},
	{domain.ErrNodeAuthDynFieldLength, "node.validation.auth_dynamic_field_length", "auth_dynamic_field"},
	{domain.ErrNodeAuthDynFieldFormat, "node.validation.auth_dynamic_field_format", "auth_dynamic_field"},
	{domain.ErrNodeInvalidAuthDynSource, "node.validation.auth_dynamic_source", "auth_dynamic_source"},
	{domain.ErrNodeTimeoutRange, "node.validation.timeout_ms", "timeout_ms"},
	{domain.ErrNodeRetryCountRange, "node.validation.retry_count", "retry_count"},
	{domain.ErrNodeRetryBackoffRange, "node.validation.retry_backoff_ms", "retry_backoff_ms"},
	{domain.ErrNodeAllowedHostsSize, "node.validation.allowed_hosts_size", "url_allowed_hosts"},
	{domain.ErrNodeForwardHeadersSize, "node.validation.forward_headers_size", "forward_headers"},
	{domain.ErrNodeWebhookSigHeaderLength, "node.validation.webhook_sig_header_length", "webhook_signature_header"},
	{domain.ErrNodeWebhookSigPrefixLength, "node.validation.webhook_sig_prefix_length", "webhook_signature_prefix"},
	{domain.ErrNodeWebhookSigHeaderRequired, "node.validation.webhook_sig_header_required", "webhook_signature_header"},
	{domain.ErrNodeWebhookSigSecretRequired, "node.validation.webhook_sig_secret_required", "incoming_auth_credentials"},
	{domain.ErrNodeInvalidTemplateID, "node.validation.template_id", "clickhouse_template_id"},
	{domain.ErrNodeExternalTableTemplateConflict, "node.validation.external_table_conflict", "clickhouse_template_id"},
	{domain.ErrNodeLogsNotConfigured, "node.validation.logs_not_configured", "clickhouse_table"},
	{domain.ErrNodeClickHouseTableInvalid, "node.validation.clickhouse_table_format", "clickhouse_table"},
	// §70.6: таблица в БД другой ноды. Не 500 и не 409 — это именно ошибка
	// значения поля: оператор указал чужое имя, и чинится оно правкой поля
	// (либо включением режима внешней таблицы §64).
	{domain.ErrNodeCHTableForeignDatabase, "node.validation.clickhouse_table_foreign", "clickhouse_table"},
	// §43.1: рендер шаблона CH-таблицы при провижене узла тоже отвергает кривое
	// имя — мапим в то же поле/сообщение (400), а не в 500/Sentry.
	{domain.ErrCHTemplateInvalidTableName, "node.validation.clickhouse_table_format", "clickhouse_table"},
	{domain.ErrNodeMaxBodySizeRange, "node.validation.max_body_size_range", "max_body_size"},
	{domain.ErrNodeMaxBodySizeRequired, "node.validation.max_body_size_required", "max_body_size"},
	{domain.ErrNodeRMQHostRequired, "node.validation.rmq_host", "rmq_host"},
	{domain.ErrNodeRMQQueueInvalid, "node.validation.rmq_queue", "rmq_queue"},
	{domain.ErrNodePullIntervalRange, "node.validation.pull_interval_sec", "pull_interval_sec"},
	{domain.ErrNodePullBatchRange, "node.validation.pull_batch_size", "pull_batch_size"},
	{domain.ErrNodePullPrefetchRange, "node.validation.pull_prefetch", "pull_prefetch"},
}

// nodeValidationCode возвращает i18n-код и имя поля для доменной ошибки
// валидации узла. ok=false — ошибка не относится к валидации полей.
func nodeValidationCode(err error) (code, field string, ok bool) {
	for _, e := range nodeValidationErrors {
		if errors.Is(err, e.err) {
			return e.code, e.field, true
		}
	}
	return "", "", false
}
