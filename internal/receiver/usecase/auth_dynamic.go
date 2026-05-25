package usecase

import (
	"net/http"
	"net/url"

	"bus/internal/domain"
)

// BuildDynamicOutgoingAuth — Phase 1.9 plug.
// Полная реализация для token_from_request / basic_from_request — следующий
// подпункт фазы. Сейчас возвращает ErrAuthTokenRequired, чтобы handler
// корректно ответил 401 на любой динамический режим.
func BuildDynamicOutgoingAuth(node *domain.Node, _ http.Header, _ url.Values, _ []byte) (string, error) {
	if !node.AuthType.IsDynamic() {
		return "", nil
	}
	return "", domain.ErrAuthTokenRequired
}
