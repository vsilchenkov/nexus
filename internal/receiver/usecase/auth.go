package usecase

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"nexus/internal/domain"
)

// CheckIncomingAuth проверяет авторизацию входящего запроса согласно
// node.IncomingAuthType (§3.3 ТЗ).
//
// В §3.3 ТЗ basic-формат: `auth_credentials = "login:password"`. Token-формат
// — просто токен. Хранятся в открытом виде в domain.Node (расшифрованные).
//
// §41: креду клиента можно брать не только из жёсткого заголовка Authorization,
// но из произвольного источника — заголовка ИЛИ query-параметра — по имени
// IncomingAuthDynamicField. Дефолт header/Authorization сохраняет прежнее
// поведение. Это read-only гейт: q не модифицируется (вырезание служебных
// значений из проксируемого запроса — задача исходящего пути, §3.5).
//
// body нужен только для IncomingAuthTypeWebhookSignature (§16 — HMAC по
// сырому телу). Для остальных типов параметр игнорируется (можно передавать
// nil). q можно передавать nil для типов, не использующих query.
func CheckIncomingAuth(node *domain.Node, h http.Header, q url.Values, body []byte) error {
	switch node.IncomingAuthType {
	case domain.IncomingAuthTypeNone:
		return nil

	case domain.IncomingAuthTypeWebhookSignature:
		return VerifyWebhookSignature(node, h, body)

	case domain.IncomingAuthTypeBasic:
		return checkIncomingBasic(node, h, q)

	case domain.IncomingAuthTypeToken:
		return checkIncomingToken(node, h, q)

	default:
		return fmt.Errorf("%w: %q", domain.ErrNodeInvalidIncomingAuthType, node.IncomingAuthType)
	}
}

// incomingAuthValue извлекает предъявленное клиентом значение креды из источника
// по IncomingAuthDynamicSource/Field (§41). Пустой source трактуется как header
// (back-compat для узлов и кэш-JSON, сохранённых до миграции 0021).
func incomingAuthValue(node *domain.Node, h http.Header, q url.Values) string {
	field := node.IncomingAuthDynamicField
	if field == "" {
		field = "Authorization"
	}
	if node.IncomingAuthDynamicSource == domain.IncomingAuthSourceQuery {
		return q.Get(field)
	}
	return h.Get(field)
}

// checkIncomingToken валидирует Bearer-токен. Для source=header сохранён прежний
// контракт: значение обязано иметь схему "Bearer <token>". Для source=query —
// значение параметра и есть токен (схемы нет). Сравнение — constant-time.
func checkIncomingToken(node *domain.Node, h http.Header, q url.Values) error {
	got := incomingAuthValue(node, h, q)
	if got == "" {
		return domain.ErrAuthHeaderMissing
	}
	token := got
	if node.IncomingAuthDynamicSource != domain.IncomingAuthSourceQuery {
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			return domain.ErrAuthHeaderMalformed
		}
		token = got[len(prefix):]
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(node.IncomingAuthCredentials)) != 1 {
		return domain.ErrUnauthorized
	}
	return nil
}

// checkIncomingBasic валидирует Basic-креду. Для source=header сохранён прежний
// контракт: "Basic <base64(login:password)>". Для source=query — значение
// параметра это сам base64(login:password) (без схемы). Сравнение — constant-time.
func checkIncomingBasic(node *domain.Node, h http.Header, q url.Values) error {
	got := incomingAuthValue(node, h, q)
	if got == "" {
		return domain.ErrAuthHeaderMissing
	}
	b64 := got
	if node.IncomingAuthDynamicSource != domain.IncomingAuthSourceQuery {
		const prefix = "Basic "
		if !strings.HasPrefix(got, prefix) {
			return domain.ErrAuthHeaderMalformed
		}
		b64 = got[len(prefix):]
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return domain.ErrAuthHeaderMalformed
	}
	if subtle.ConstantTimeCompare(raw, []byte(node.IncomingAuthCredentials)) != 1 {
		return domain.ErrUnauthorized
	}
	return nil
}

// BuildOutgoingAuth формирует значение Authorization-заголовка для outbound-запроса
// в соответствии с node.AuthType (§3.5 ТЗ). Возвращает пустую строку, если
// заголовок ставить не нужно (auth_type=none).
//
// Динамические режимы (token_from_request, basic_from_request) обрабатываются
// в Phase 1.9 — отдельная функция, потому что им нужен доступ к http.Request.
func BuildOutgoingAuth(node *domain.Node) (string, error) {
	switch node.AuthType {
	case domain.AuthTypeNone:
		return "", nil

	case domain.AuthTypeBasic:
		// node.AuthCredentials хранит "login:password".
		enc := base64.StdEncoding.EncodeToString([]byte(node.AuthCredentials))
		return "Basic " + enc, nil

	case domain.AuthTypeToken:
		return "Bearer " + node.AuthCredentials, nil

	case domain.AuthTypeTokenFromRequest, domain.AuthTypeBasicFromRequest:
		// Phase 1.9 — динамический режим. На текущем этапе пусть запрос
		// не доходит сюда; роутер обрабатывает динамику отдельно.
		return "", fmt.Errorf("dynamic auth_type must be resolved via BuildDynamicOutgoingAuth")

	default:
		return "", fmt.Errorf("%w: %q", domain.ErrNodeInvalidAuthType, node.AuthType)
	}
}
