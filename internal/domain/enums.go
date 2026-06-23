// Package domain — общая доменная модель шины.
//
// По §17.2 ТЗ domain — самый внутренний слой: только структуры и инварианты,
// никаких зависимостей наружу. Структуры Node/User/LogRecord нужны во всех
// трёх сервисах одинаково, поэтому вынесены в общий пакет — это shared
// kernel, не нарушающий Clean (зависимости всё равно направлены внутрь).
package domain

// NodeStatus — состояние узла (§3.6).
type NodeStatus string

const (
	NodeStatusEnabled  NodeStatus = "enabled"
	NodeStatusDisabled NodeStatus = "disabled"
	NodeStatusPaused   NodeStatus = "paused"
)

func (s NodeStatus) Valid() bool {
	switch s {
	case NodeStatusEnabled, NodeStatusDisabled, NodeStatusPaused:
		return true
	}
	return false
}

// RootMethod — корневой метод узла.
type RootMethod string

const (
	RootMethodRequest       RootMethod = "request"
	RootMethodRequestAsync  RootMethod = "requestAsync"
	RootMethodRabbitMQAsync RootMethod = "RabbitMQAsync"
)

func (m RootMethod) Valid() bool {
	switch m {
	case RootMethodRequest, RootMethodRequestAsync, RootMethodRabbitMQAsync:
		return true
	}
	return false
}

// IsPull сообщает, что узел сам забирает сообщения из внешнего источника
// (RabbitMQAsync, §27), а не ждёт входящего HTTP-запроса. Для pull-узлов
// нет смысла в incoming-авторизации и url_mode=from_request, и только для
// них применимо runtime-состояние degraded.
func (m RootMethod) IsPull() bool { return m == RootMethodRabbitMQAsync }

// HTTPMethod — HTTP-метод узла (§3.2): отдельно для входящего запроса
// (который узел принимает) и для исходящего вызова получателя. По умолчанию
// POST. Кроме четырёх конкретных методов есть «Любой» (ANY, §40): для входящего
// — принимать запрос с любым методом (без 405); для исходящего — вызывать
// получателя тем же методом, что пришёл от клиента (зеркало источника).
type HTTPMethod string

const (
	HTTPMethodGET    HTTPMethod = "GET"
	HTTPMethodPOST   HTTPMethod = "POST"
	HTTPMethodPUT    HTTPMethod = "PUT"
	HTTPMethodDELETE HTTPMethod = "DELETE"
	// HTTPMethodAny — §40 «Любой». Вх: узел принимает любой метод. Исх: Sender
	// зеркалит метод входящего запроса (для pull-узлов — fallback POST).
	HTTPMethodAny HTTPMethod = "ANY"
)

func (m HTTPMethod) Valid() bool {
	switch m {
	case HTTPMethodGET, HTTPMethodPOST, HTTPMethodPUT, HTTPMethodDELETE, HTTPMethodAny:
		return true
	}
	return false
}

// URLMode — режим определения целевого URL (§3.4).
type URLMode string

const (
	URLModeStatic      URLMode = "static"
	URLModeFromRequest URLMode = "from_request"
)

func (u URLMode) Valid() bool {
	return u == URLModeStatic || u == URLModeFromRequest
}

// AuthType — исходящая авторизация (§3.5).
type AuthType string

const (
	AuthTypeNone             AuthType = "none"
	AuthTypeBasic            AuthType = "basic"
	AuthTypeToken            AuthType = "token"
	AuthTypeTokenFromRequest AuthType = "token_from_request"
	AuthTypeBasicFromRequest AuthType = "basic_from_request"
)

func (a AuthType) Valid() bool {
	switch a {
	case AuthTypeNone, AuthTypeBasic, AuthTypeToken,
		AuthTypeTokenFromRequest, AuthTypeBasicFromRequest:
		return true
	}
	return false
}

// IsDynamic — true для режимов, где креды извлекаются из входящего запроса.
func (a AuthType) IsDynamic() bool {
	return a == AuthTypeTokenFromRequest || a == AuthTypeBasicFromRequest
}

// AuthDynSource — источник динамической авторизации.
type AuthDynSource string

const (
	AuthDynSourceQuery  AuthDynSource = "query"
	AuthDynSourceHeader AuthDynSource = "header"
	AuthDynSourceBody   AuthDynSource = "body"
)

func (s AuthDynSource) Valid() bool {
	switch s {
	case AuthDynSourceQuery, AuthDynSourceHeader, AuthDynSourceBody:
		return true
	}
	return false
}

// IncomingAuthType — авторизация запросов на вход в шину.
type IncomingAuthType string

const (
	IncomingAuthTypeNone             IncomingAuthType = "none"
	IncomingAuthTypeBasic            IncomingAuthType = "basic"
	IncomingAuthTypeToken            IncomingAuthType = "token"
	IncomingAuthTypeWebhookSignature IncomingAuthType = "webhook_signature"
)

func (a IncomingAuthType) Valid() bool {
	switch a {
	case IncomingAuthTypeNone, IncomingAuthTypeBasic, IncomingAuthTypeToken,
		IncomingAuthTypeWebhookSignature:
		return true
	}
	return false
}

// IncomingAuthSource — источник креды для входящей авторизации (§41):
// заголовок (по умолчанию — прежнее поведение, читается Authorization) или
// query-параметр. В отличие от исходящей AuthDynSource, 'body' не поддерживается
// (для гейта на входе нет реального кейса).
type IncomingAuthSource string

const (
	IncomingAuthSourceHeader IncomingAuthSource = "header"
	IncomingAuthSourceQuery  IncomingAuthSource = "query"
)

func (s IncomingAuthSource) Valid() bool {
	switch s {
	case IncomingAuthSourceHeader, IncomingAuthSourceQuery:
		return true
	}
	return false
}

// UserRole — роль пользователя UI (§7.1, §26).
//
// Иерархия прав: viewer < manager < admin (см. Rank/AtLeast). Менеджер
// управляет узлами и каталогами Allowed Hosts/Headers, видит Audit log и
// меняет только свой пароль; общие настройки, пользователи, команды и
// шаблоны CH остаются за admin (§26).
type UserRole string

const (
	UserRoleAdmin   UserRole = "admin"
	UserRoleManager UserRole = "manager"
	UserRoleViewer  UserRole = "viewer"
)

func (r UserRole) Valid() bool {
	switch r {
	case UserRoleAdmin, UserRoleManager, UserRoleViewer:
		return true
	}
	return false
}

func (r UserRole) IsAdmin() bool { return r == UserRoleAdmin }

// Rank — числовой ранг роли в иерархии (viewer=0, manager=1, admin=2).
// Неизвестная роль трактуется как минимальный ранг.
func (r UserRole) Rank() int {
	switch r {
	case UserRoleAdmin:
		return 2
	case UserRoleManager:
		return 1
	default:
		return 0
	}
}

// AtLeast сообщает, что роль не ниже min по иерархии прав.
func (r UserRole) AtLeast(min UserRole) bool { return r.Rank() >= min.Rank() }

// UserLang — язык UI пользователя (§7.11).
type UserLang string

const (
	UserLangEN UserLang = "en"
	UserLangRU UserLang = "ru"
)

func (l UserLang) Valid() bool { return l == UserLangEN || l == UserLangRU }
