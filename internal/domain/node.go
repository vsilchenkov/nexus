package domain

import (
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"
)

// Node — узел перенаправления (§3.3 ТЗ).
//
// Поля auth_credentials / incoming_auth_credentials хранятся в БД
// зашифрованными (AES-256-GCM, §5.5). В domain.Node они представлены как
// plaintext-строки; шифрование/расшифровка происходит в adapter/out/postgres
// при чтении и записи. То есть usecase и handlers всегда работают с
// открытыми значениями, postgres adapter — единственное место, где знают
// про шифр.
type Node struct {
	ID         string
	Path       string
	RootMethod RootMethod

	// §3.2: HTTP-метод узла. IncomingMethod — метод, который узел принимает на
	// вход (иначе 405); OutgoingMethod — метод, которым Sender вызывает
	// получателя. Оба по умолчанию POST. Для pull-узлов (RabbitMQAsync)
	// IncomingMethod неприменим (входящего HTTP-запроса нет).
	IncomingMethod HTTPMethod
	OutgoingMethod HTTPMethod

	URLMode         URLMode
	TargetURL       string
	URLParamName    string
	URLAllowedHosts []string

	AuthType               AuthType
	AuthCredentials        string // plaintext в памяти, шифр в БД
	AuthDynamicSource      AuthDynSource
	AuthDynamicField       string
	AuthDynamicStripPrefix string

	IncomingAuthType        IncomingAuthType
	IncomingAuthCredentials string // plaintext в памяти, шифр в БД

	// Параметры webhook-подписи (§16 ТЗ, IncomingAuthType="webhook_signature").
	// Сам секрет лежит в IncomingAuthCredentials.
	WebhookSignatureHeader string // имя HTTP-заголовка, напр. "X-Hub-Signature-256"
	WebhookSignaturePrefix string // префикс, отрезается перед hex-decode, напр. "sha256="

	ForwardHeaders          []string
	TimeoutMs               int32
	RetryCount              int32
	RetryBackoffMs          int32
	ClickHouseTable         string
	ClickHouseTemplateID    string // §19: FK на ch_templates; пусто = ручная таблица (legacy)
	ClickHouseRetentionDays int32  // §4.3: TTL по партициям (housekeeping)

	Status NodeStatus
	TeamID string

	LogRequestBody  bool
	LogResponseBody bool
	LogHeaders      bool

	// §22: контроль логирования на уровне узла.
	// LoggingEnabled=false — узел не пишет лог в ClickHouse совсем.
	// При MaxBodySizeEnabled сохраняемые request/response режутся до
	// MaxBodySize СИМВОЛОВ (рун); checksum считается по полному телу.
	LoggingEnabled     bool
	MaxBodySizeEnabled bool
	MaxBodySize        int32

	// §27: параметры узла RabbitMQAsync (root_method="RabbitMQAsync").
	// Для request/requestAsync эти поля пустые/нулевые. RMQPassword хранится
	// в БД зашифрованным (AES-256-GCM, как AuthCredentials); в domain.Node —
	// plaintext. Заполнены и осмысленны только при RootMethod.IsPull().
	RMQHost         string
	RMQPort         int32
	RMQVHost        string
	RMQUser         string
	RMQPassword     string // plaintext в памяти, шифр в БД
	RMQQueue        string
	RMQUseTLS       bool
	PullIntervalSec int32
	PullBatchSize   int32
	PullPrefetch    int32

	// §29: произвольный комментарий-описание узла (UI-метаданные, не участвует
	// в маршрутизации). Необязательное, максимум 2000 символов.
	Comment string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// UnmarshalJSON задаёт дефолт LoggingEnabled=true для JSON без этого поля (§22).
// Узел сериализуется в Redis-кеш как JSON; записи, сохранённые до появления
// поля (например, переживший выкат L1-кеш ресивера), не должны выключать
// логирование. Отсутствующее поле → true, явное значение — как прислано.
func (n *Node) UnmarshalJSON(data []byte) error {
	type alias Node // без методов Node, чтобы не зациклить UnmarshalJSON
	aux := &struct {
		LoggingEnabled *bool `json:"LoggingEnabled"`
		*alias
	}{alias: (*alias)(n)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	n.LoggingEnabled = aux.LoggingEnabled == nil || *aux.LoggingEnabled
	return nil
}

// NormalizeForRootMethod приводит узел к виду, совместимому с его RootMethod,
// и возвращает имена молча сброшенных несовместимых полей (для audit, §27.6).
// Для pull-узлов (RabbitMQAsync) нет входящего HTTP-запроса: incoming-авторизация
// и url_mode=from_request бессмысленны, динамическая исходящая авторизация
// (*_from_request) тоже — её неоткуда брать. Вызывается в usecase.Create/Update
// до Validate.
func (n *Node) NormalizeForRootMethod() []string {
	if !n.RootMethod.IsPull() {
		return nil
	}
	var cleared []string
	if n.IncomingAuthType != "" && n.IncomingAuthType != IncomingAuthTypeNone {
		n.IncomingAuthType = IncomingAuthTypeNone
		n.IncomingAuthCredentials = ""
		n.WebhookSignatureHeader = ""
		cleared = append(cleared, "incoming_auth_type")
	}
	if n.URLMode == URLModeFromRequest {
		n.URLMode = URLModeStatic
		cleared = append(cleared, "url_mode")
	}
	if n.AuthType.IsDynamic() {
		n.AuthType = AuthTypeNone
		n.AuthCredentials = ""
		cleared = append(cleared, "auth_type")
	}
	return cleared
}

// pathPattern — то же ограничение, что в БД-constraint (§3.3 ТЗ).
var pathPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_-]*$`)
var paramNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// rmqQueuePattern — стандартное ограничение имени очереди RabbitMQ (§27.6).
var rmqQueuePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Validate проверяет доменные инварианты узла. Используется в usecase.Create/Update.
// Constraints в БД — второй уровень защиты; здесь — основной, потому что только
// тут можно вернуть user-friendly ошибку до похода в БД.
func (n *Node) Validate() error {
	if !n.RootMethod.Valid() {
		return ErrNodeInvalidRootMethod
	}
	// Пустой метод допустим — это «использовать дефолт POST» (SetDefaults
	// заполнит, БД-колонка NOT NULL DEFAULT 'POST', methodMatches трактует
	// пустое как POST). Непустое значение должно быть из допустимого множества.
	if n.IncomingMethod != "" && !n.IncomingMethod.Valid() {
		return ErrNodeInvalidIncomingMethod
	}
	if n.OutgoingMethod != "" && !n.OutgoingMethod.Valid() {
		return ErrNodeInvalidOutgoingMethod
	}
	if !n.URLMode.Valid() {
		return ErrNodeInvalidURLMode
	}
	if !n.AuthType.Valid() {
		return ErrNodeInvalidAuthType
	}
	if !n.IncomingAuthType.Valid() {
		return ErrNodeInvalidIncomingAuthType
	}
	if !n.Status.Valid() {
		return ErrNodeInvalidStatus
	}
	if l := len(n.Path); l < 1 || l > 255 {
		return ErrNodePathLength
	}
	if !pathPattern.MatchString(n.Path) {
		return ErrNodePathFormat
	}
	if n.URLMode == URLModeStatic && n.TargetURL == "" {
		return ErrNodeStaticNeedsTargetURL
	}
	if l := len(n.TargetURL); l > 2048 {
		return ErrNodeTargetURLLength
	}
	if l := len(n.URLParamName); l < 1 || l > 64 {
		return ErrNodeParamNameLength
	}
	if !paramNamePattern.MatchString(n.URLParamName) {
		return ErrNodeParamNameFormat
	}
	if l := len(n.AuthDynamicField); l < 1 || l > 64 {
		return ErrNodeAuthDynFieldLength
	}
	if !paramNamePattern.MatchString(n.AuthDynamicField) {
		return ErrNodeAuthDynFieldFormat
	}
	if n.AuthType == AuthTypeTokenFromRequest && !n.AuthDynamicSource.Valid() {
		return ErrNodeInvalidAuthDynSource
	}
	if n.TimeoutMs < 100 || n.TimeoutMs > 300_000 {
		return ErrNodeTimeoutRange
	}
	if n.RetryCount < 0 || n.RetryCount > 10 {
		return ErrNodeRetryCountRange
	}
	if n.RetryBackoffMs < 0 || n.RetryBackoffMs > 60_000 {
		return ErrNodeRetryBackoffRange
	}
	if len(n.URLAllowedHosts) > 50 {
		return ErrNodeAllowedHostsSize
	}
	if len(n.ForwardHeaders) > 30 {
		return ErrNodeForwardHeadersSize
	}
	if l := len(n.WebhookSignatureHeader); l > 128 {
		return ErrNodeWebhookSigHeaderLength
	}
	if l := len(n.WebhookSignaturePrefix); l > 64 {
		return ErrNodeWebhookSigPrefixLength
	}
	if n.IncomingAuthType == IncomingAuthTypeWebhookSignature {
		if n.WebhookSignatureHeader == "" {
			return ErrNodeWebhookSigHeaderRequired
		}
		if n.IncomingAuthCredentials == "" {
			return ErrNodeWebhookSigSecretRequired
		}
	}
	if n.ClickHouseTemplateID != "" && !uuidPattern.MatchString(n.ClickHouseTemplateID) {
		return ErrNodeInvalidTemplateID
	}
	if n.MaxBodySize < 0 || n.MaxBodySize > 10_000_000 {
		return ErrNodeMaxBodySizeRange
	}
	// §29: лимит по рунам (символам) — совпадает с PG CHECK length(comment) и
	// с DTO-binding max (validator считает руны), без расхождений для кириллицы.
	if utf8.RuneCountInString(n.Comment) > 2000 {
		return ErrNodeCommentLength
	}
	if n.MaxBodySizeEnabled && n.MaxBodySize <= 0 {
		return ErrNodeMaxBodySizeRequired
	}
	// §28 Пункт 5: логирование включено, но не настроено имя таблицы. Без него
	// Sender некуда писать логи, а UI-чтение логов/метрик падает с CH code 60.
	if n.LoggingEnabled && n.ClickHouseTable == "" {
		return ErrNodeLogsNotConfigured
	}
	if n.RootMethod.IsPull() {
		if err := n.validateRMQ(); err != nil {
			return err
		}
	}
	return nil
}

// validateRMQ проверяет инварианты узла RabbitMQAsync (§27.6). Дублирует
// БД-constraint chk_rmq_fields, но даёт user-friendly ошибку до похода в БД.
func (n *Node) validateRMQ() error {
	if l := len(n.RMQHost); l < 1 || l > 253 {
		return ErrNodeRMQHostRequired
	}
	if l := len(n.RMQQueue); l < 1 || l > 255 || !rmqQueuePattern.MatchString(n.RMQQueue) {
		return ErrNodeRMQQueueInvalid
	}
	if n.PullIntervalSec < 1 || n.PullIntervalSec > 3600 {
		return ErrNodePullIntervalRange
	}
	if n.PullBatchSize < 1 || n.PullBatchSize > 1000 {
		return ErrNodePullBatchRange
	}
	if n.PullPrefetch < 1 || n.PullPrefetch > 1000 {
		return ErrNodePullPrefetchRange
	}
	return nil
}

// SetDefaults заполняет zero-поля значениями по умолчанию из §3.3.
// Вызывается до Validate в usecase.Create — чтобы пользователь мог
// прислать минимальный JSON и получить рабочий узел.
func (n *Node) SetDefaults() {
	if n.IncomingMethod == "" {
		n.IncomingMethod = HTTPMethodPOST
	}
	if n.OutgoingMethod == "" {
		n.OutgoingMethod = HTTPMethodPOST
	}
	if n.URLMode == "" {
		n.URLMode = URLModeStatic
	}
	if n.URLParamName == "" {
		n.URLParamName = "url_base"
	}
	if n.AuthType == "" {
		n.AuthType = AuthTypeNone
	}
	if n.AuthDynamicSource == "" {
		n.AuthDynamicSource = AuthDynSourceQuery
	}
	if n.AuthDynamicField == "" {
		n.AuthDynamicField = "token"
	}
	if n.AuthDynamicStripPrefix == "" && n.AuthType == AuthTypeTokenFromRequest && n.AuthDynamicSource == AuthDynSourceHeader {
		n.AuthDynamicStripPrefix = "Bearer "
	}
	if n.IncomingAuthType == "" {
		n.IncomingAuthType = IncomingAuthTypeNone
	}
	if n.Status == "" {
		n.Status = NodeStatusEnabled
	}
	// n.TeamID — обязательное UUID-поле в multi-tenancy v2 (миграция 0008).
	// Резолв "current team" — задача handler'а / usecase, не SetDefaults
	// (см. NodeUsecase.defaultTeamID для legacy single-team пути).
	if n.TimeoutMs == 0 {
		n.TimeoutMs = 30_000
	}
	if n.RetryBackoffMs == 0 {
		n.RetryBackoffMs = 1_000
	}
	if n.ClickHouseRetentionDays == 0 {
		n.ClickHouseRetentionDays = 90
	}
	if n.RootMethod.IsPull() {
		// (см. NormalizeForRootMethod — вызывается отдельно в usecase,
		// чтобы зафиксировать сброшенные поля в audit)
		if n.RMQVHost == "" {
			n.RMQVHost = "/"
		}
		if n.RMQPort == 0 {
			if n.RMQUseTLS {
				n.RMQPort = 5671
			} else {
				n.RMQPort = 5672
			}
		}
		if n.PullIntervalSec == 0 {
			n.PullIntervalSec = 5
		}
		if n.PullBatchSize == 0 {
			n.PullBatchSize = 100
		}
		if n.PullPrefetch == 0 {
			n.PullPrefetch = n.PullBatchSize
		}
	}
}
