package domain

import (
	"encoding/json"
	"regexp"
	"strings"
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

	// §39: path-passthrough. Когда true, хвост входящего пути после пути узла
	// приклеивается к резолвнутому целевому URL (для url_mode=static и
	// from_request). По умолчанию false — точный матч пути (текущее поведение).
	// Неприменимо к pull-узлам (RabbitMQAsync) — нет входящего HTTP-пути.
	PathPassthrough bool

	AuthType               AuthType
	AuthCredentials        string // plaintext в памяти, шифр в БД
	AuthDynamicSource      AuthDynSource
	AuthDynamicField       string
	AuthDynamicStripPrefix string

	IncomingAuthType        IncomingAuthType
	IncomingAuthCredentials string // plaintext в памяти, шифр в БД

	// §41: источник и имя поля для входящей динамической авторизации
	// (basic/token). Source=header (дефолт) + Field=Authorization → прежнее
	// поведение (читать заголовок Authorization). Source=query — брать креду
	// из query-параметра с именем Field. 'body' для входа не поддерживается.
	IncomingAuthDynamicSource IncomingAuthSource
	IncomingAuthDynamicField  string

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

	// §64: внешняя (ручная) таблица логов. true — Nexus не управляет таблицей
	// совсем: не создаёт и не переименовывает её, не применяет стартовые ALTER
	// миграции схемы, не дропает партиции по retention и не даёт запустить
	// §56 schema-sync. Нужно, когда в таблицу пишет посторонний сервис, а Nexus
	// работает только как фронт чтения логов. Несовместимо с ClickHouseTemplateID:
	// шаблон означает «таблицей управляет Nexus».
	ExternalTable bool

	DLQTTLSeconds        int32 // §36: TTL повторной доставки неудачных async-сообщений из DLQ (секунды)
	DLQRetryDelaySeconds int32 // §36: минимальная задержка перед повторной доставкой ошибочной отправки (секунды)

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

	// §63: логин автора создания и последнего изменения узла (для показа рядом
	// с «Создано»/«Обновлено»). CreatedBy пишется при создании и не меняется;
	// UpdatedBy — при каждом изменении узла. Пусто у узлов до миграции 0026.
	CreatedBy string
	UpdatedBy string
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
	if n.PathPassthrough {
		// §39: у pull-узла нет входящего HTTP-пути, приклеивать нечего.
		n.PathPassthrough = false
		cleared = append(cleared, "path_passthrough")
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
//
// Длина функции осознанна: это последовательность независимых проверок полей,
// каждая со своей доменной ошибкой. Разнесение по под-функциям («проверки URL»,
// «проверки auth») только спрятало бы порядок, в котором пользователь получает
// первую ошибку, — а он и определяет, что подсветит форма.
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
	// §69.2: в режиме static (в него же нормализуются pull-узлы) target_url —
	// фактический адрес доставки, поэтому обязан быть абсолютным http(s).
	// У from_request поле не используется (адрес приходит в запросе) и на форме
	// скрыто: остаточное значение от переключения режима не должно блокировать
	// сохранение ошибкой на невидимом поле. Пустое значение здесь не трогаем —
	// обязательность закрыта ErrNodeStaticNeedsTargetURL выше.
	if n.URLMode == URLModeStatic && n.TargetURL != "" {
		if _, ok := AbsoluteHTTPURL(n.TargetURL); !ok {
			return ErrNodeTargetURLScheme
		}
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
	// §41: источник обязателен для обоих динамических исходящих режимов
	// (token_from_request и basic_from_request — последний теперь тоже
	// настраиваемый по source/field).
	if n.AuthType.IsDynamic() && !n.AuthDynamicSource.Valid() {
		return ErrNodeInvalidAuthDynSource
	}
	// §41: входящая динамическая авторизация. Пустые source/field трактуются
	// как дефолт header/Authorization (согласовано с SetDefaults и runtime
	// incomingAuthValue) — поэтому проверяем только непустые значения, чтобы не
	// ломать узлы, построенные без SetDefaults. Обязательность выбора поля для
	// динамических режимов — UX-гейт фронта (на бэке всегда есть дефолт).
	if n.IncomingAuthDynamicSource != "" && !n.IncomingAuthDynamicSource.Valid() {
		return ErrNodeInvalidIncomingAuthDynSource
	}
	if n.IncomingAuthDynamicField != "" {
		if len(n.IncomingAuthDynamicField) > 64 {
			return ErrNodeIncomingAuthDynFieldLength
		}
		if !paramNamePattern.MatchString(n.IncomingAuthDynamicField) {
			return ErrNodeIncomingAuthDynFieldFormat
		}
	}
	if n.TimeoutMs < 100 || n.TimeoutMs > 600_000 {
		return ErrNodeTimeoutRange
	}
	if n.RetryCount < 0 || n.RetryCount > 10 {
		return ErrNodeRetryCountRange
	}
	if n.RetryBackoffMs < 0 || n.RetryBackoffMs > 60_000 {
		return ErrNodeRetryBackoffRange
	}
	if n.DLQTTLSeconds < 60 || n.DLQTTLSeconds > 2_592_000 {
		return ErrNodeDLQTTLRange
	}
	if n.DLQRetryDelaySeconds < 1 || n.DLQRetryDelaySeconds > 86_400 {
		return ErrNodeDLQRetryDelayRange
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
	// §64: шаблон = «таблицей управляет Nexus», external_table = «таблицу не
	// трогаем». Вместе бессмысленны: провижининг по шаблону всё равно не
	// выполнится, а оператор считал бы, что схема поддерживается.
	if n.ExternalTable && n.ClickHouseTemplateID != "" {
		return ErrNodeExternalTableTemplateConflict
	}
	if n.MaxBodySize < 0 || n.MaxBodySize > 10_000_000 {
		return ErrNodeMaxBodySizeRange
	}
	// §29: лимит по рунам (символам) — совпадает с PG CHECK length(comment) и
	// с DTO-binding max (validator считает руны), без расхождений для кириллицы.
	if utf8.RuneCountInString(n.Comment) > 2000 {
		return ErrNodeCommentLength
	}
	// Гейт по LoggingEnabled: при выключенном логировании поля карточки
	// «Логирование» задизейблены в UI — блокирующая ошибка по ним была бы
	// ловушкой (исправить нельзя, не включив логи). Недозаполненное имя
	// таблицы (например, дефолтный префикс «nexus_x.» формы создания)
	// хранится как черновик: узел с выключенными логами таблицу не пишет и
	// не читает, а при включении логов валидация потребует поправить.
	if n.LoggingEnabled && n.MaxBodySizeEnabled && n.MaxBodySize <= 0 {
		return ErrNodeMaxBodySizeRequired
	}
	// §28 Пункт 5: логирование включено, но не настроено имя таблицы. Без него
	// Sender некуда писать логи, а UI-чтение логов/метрик падает с CH code 60.
	if n.LoggingEnabled && n.ClickHouseTable == "" {
		return ErrNodeLogsNotConfigured
	}
	// §42: имя таблицы должно быть строго db.table из [A-Za-z0-9_]. Иначе
	// Sender пишет, а UI-чтение падает позже на «invalid table name» —
	// ловим на сохранении и показываем понятную ошибку у поля.
	if n.LoggingEnabled && n.ClickHouseTable != "" && !IsValidCHTableName(n.ClickHouseTable) {
		return ErrNodeClickHouseTableInvalid
	}
	if n.RootMethod.IsPull() {
		if err := n.validateRMQ(); err != nil {
			return err
		}
	}
	return nil
}

// IsValidCHTableName — имя в формате db.table из [A-Za-z0-9_]: обе части
// обязательны, ровно одна точка. Зеркалит isSafeTableName в ClickHouse-адаптерах
// (read/write). Экспортирован, чтобы read-слой (логи/метрики) мог заранее отсеять
// узлы с кривым именем таблицы и деградировать мягко, а не флудить Sentry
// «invalid table name» на каждом поллинге (§43.1).
func IsValidCHTableName(name string) bool {
	dot := -1
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '.':
			if dot >= 0 {
				return false
			}
			dot = i
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return dot > 0 && dot < len(name)-1
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
	// §69.2: пробелы по краям адреса — частая опечатка копипаста; режем до
	// валидации, иначе " https://host" уедет в конверт и упадёт в Sender'е.
	n.TargetURL = strings.TrimSpace(n.TargetURL)
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
	// §41: дефолты источника/поля исходящей динамической авторизации зависят от
	// режима. basic_from_request исторически читал заголовок Authorization
	// (прозрачный проброс) — для него дефолт header/Authorization, чтобы новый
	// source/field-aware код вёл себя как раньше. token_from_request — query/token.
	if n.AuthDynamicSource == "" {
		if n.AuthType == AuthTypeBasicFromRequest {
			n.AuthDynamicSource = AuthDynSourceHeader
		} else {
			n.AuthDynamicSource = AuthDynSourceQuery
		}
	}
	if n.AuthDynamicField == "" {
		if n.AuthType == AuthTypeBasicFromRequest {
			n.AuthDynamicField = "Authorization"
		} else {
			n.AuthDynamicField = "token"
		}
	}
	if n.AuthDynamicStripPrefix == "" && n.AuthType == AuthTypeTokenFromRequest && n.AuthDynamicSource == AuthDynSourceHeader {
		n.AuthDynamicStripPrefix = "Bearer "
	}
	if n.IncomingAuthType == "" {
		n.IncomingAuthType = IncomingAuthTypeNone
	}
	// §41: дефолты header/Authorization сохраняют прежнее поведение basic/token
	// и старого кэш-JSON без этих полей (zero-value → header/Authorization).
	if n.IncomingAuthDynamicSource == "" {
		n.IncomingAuthDynamicSource = IncomingAuthSourceHeader
	}
	if n.IncomingAuthDynamicField == "" {
		n.IncomingAuthDynamicField = "Authorization"
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
	if n.DLQTTLSeconds == 0 {
		n.DLQTTLSeconds = 86_400 // §36: 24ч по умолчанию
	}
	if n.DLQRetryDelaySeconds == 0 {
		n.DLQRetryDelaySeconds = 300 // §36: 5 мин по умолчанию (= интервал прохода)
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
