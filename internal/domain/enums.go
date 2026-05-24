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
	RootMethodRequest      RootMethod = "request"
	RootMethodRequestAsync RootMethod = "requestAsync"
)

func (m RootMethod) Valid() bool {
	return m == RootMethodRequest || m == RootMethodRequestAsync
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
	IncomingAuthTypeNone  IncomingAuthType = "none"
	IncomingAuthTypeBasic IncomingAuthType = "basic"
	IncomingAuthTypeToken IncomingAuthType = "token"
)

func (a IncomingAuthType) Valid() bool {
	switch a {
	case IncomingAuthTypeNone, IncomingAuthTypeBasic, IncomingAuthTypeToken:
		return true
	}
	return false
}

// UserRole — роль пользователя UI (§7.1).
type UserRole string

const (
	UserRoleAdmin  UserRole = "admin"
	UserRoleViewer UserRole = "viewer"
)

func (r UserRole) Valid() bool   { return r == UserRoleAdmin || r == UserRoleViewer }
func (r UserRole) IsAdmin() bool { return r == UserRoleAdmin }

// UserLang — язык UI пользователя (§7.11).
type UserLang string

const (
	UserLangEN UserLang = "en"
	UserLangRU UserLang = "ru"
)

func (l UserLang) Valid() bool { return l == UserLangEN || l == UserLangRU }
