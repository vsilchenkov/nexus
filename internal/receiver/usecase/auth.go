package usecase

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"bus/internal/domain"
)

// CheckIncomingAuth проверяет авторизацию входящего запроса согласно
// node.IncomingAuthType (§3.3 ТЗ).
//
// В §3.3 ТЗ basic-формат: `auth_credentials = "login:password"`. Token-формат
// — просто токен. Хранятся в открытом виде в domain.Node (расшифрованные).
func CheckIncomingAuth(node *domain.Node, h http.Header) error {
	switch node.IncomingAuthType {
	case domain.IncomingAuthTypeNone:
		return nil

	case domain.IncomingAuthTypeBasic:
		got := h.Get("Authorization")
		if got == "" {
			return domain.ErrAuthHeaderMissing
		}
		const prefix = "Basic "
		if !strings.HasPrefix(got, prefix) {
			return domain.ErrAuthHeaderMalformed
		}
		raw, err := base64.StdEncoding.DecodeString(got[len(prefix):])
		if err != nil {
			return domain.ErrAuthHeaderMalformed
		}
		expected := []byte(node.IncomingAuthCredentials)
		if subtle.ConstantTimeCompare(raw, expected) != 1 {
			return domain.ErrUnauthorized
		}
		return nil

	case domain.IncomingAuthTypeToken:
		got := h.Get("Authorization")
		if got == "" {
			return domain.ErrAuthHeaderMissing
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			return domain.ErrAuthHeaderMalformed
		}
		token := got[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(token), []byte(node.IncomingAuthCredentials)) != 1 {
			return domain.ErrUnauthorized
		}
		return nil

	default:
		return fmt.Errorf("%w: %q", domain.ErrNodeInvalidIncomingAuthType, node.IncomingAuthType)
	}
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
