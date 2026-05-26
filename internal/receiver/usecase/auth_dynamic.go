package usecase

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"nexus/internal/domain"
)

// DynamicAuthResult — результат разбора динамической авторизации (§3.5).
//
//	Header — готовая строка для outbound-заголовка Authorization
//	         ("Bearer <token>" или "Basic <base64>").
//
//	Stripped — обновлённые copies входящих query/headers/body, из которых
//	           удалены служебные значения, чтобы они не попали внешнему
//	           узлу (§3.5 «Исключение из проксируемого запроса»).
type DynamicAuthResult struct {
	Header  string
	Query   url.Values
	Headers http.Header
	Body    []byte
}

// BuildDynamicOutgoingAuth обрабатывает оба динамических режима.
// Для статических режимов (none/basic/token) возвращает nil — вызывающий
// должен использовать BuildOutgoingAuth.
func BuildDynamicOutgoingAuth(
	node *domain.Node,
	in http.Header,
	q url.Values,
	body []byte,
) (*DynamicAuthResult, error) {
	switch node.AuthType {
	case domain.AuthTypeTokenFromRequest:
		return buildTokenFromRequest(node, in, q, body)
	case domain.AuthTypeBasicFromRequest:
		return buildBasicFromRequest(in, q, body)
	default:
		return nil, fmt.Errorf("%w: %q is not dynamic", domain.ErrNodeInvalidAuthType, node.AuthType)
	}
}

func buildTokenFromRequest(
	node *domain.Node,
	in http.Header,
	q url.Values,
	body []byte,
) (*DynamicAuthResult, error) {
	field := node.AuthDynamicField
	if field == "" {
		return nil, fmt.Errorf("%w: auth_dynamic_field empty", domain.ErrAuthTokenRequired)
	}

	res := &DynamicAuthResult{
		Query:   cloneValues(q),
		Headers: cloneHeader(in),
		Body:    body,
	}

	var token string
	switch node.AuthDynamicSource {
	case domain.AuthDynSourceQuery:
		token = res.Query.Get(field)
		res.Query.Del(field)
	case domain.AuthDynSourceHeader:
		token = res.Headers.Get(field)
		res.Headers.Del(field)
		if prefix := node.AuthDynamicStripPrefix; prefix != "" {
			token = strings.TrimPrefix(token, prefix)
		}
	case domain.AuthDynSourceBody:
		var stripped []byte
		var err error
		token, stripped, err = extractAndStripJSONField(body, field)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", domain.ErrAuthTokenRequired, err)
		}
		res.Body = stripped
	default:
		return nil, fmt.Errorf("%w: %q", domain.ErrNodeInvalidAuthDynSource, node.AuthDynamicSource)
	}

	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: %s/%s", domain.ErrAuthTokenRequired,
			node.AuthDynamicSource, field)
	}
	res.Header = "Bearer " + token
	return res, nil
}

func buildBasicFromRequest(in http.Header, q url.Values, body []byte) (*DynamicAuthResult, error) {
	got := in.Get("Authorization")
	const prefix = "Basic "
	if got == "" || !strings.HasPrefix(got, prefix) {
		return nil, domain.ErrAuthHeaderMissing
	}
	// Валидируем base64 (без использования значения — только проверка).
	if _, err := base64.StdEncoding.DecodeString(got[len(prefix):]); err != nil {
		return nil, domain.ErrAuthHeaderMalformed
	}

	res := &DynamicAuthResult{
		Header:  got,
		Query:   cloneValues(q),
		Headers: cloneHeader(in),
		Body:    body,
	}
	// Исходный заголовок не дублируем в проксируемый запрос;
	// outbound-заголовок Authorization подставит RouteUsecase
	// из res.Header (§3.5 «Исключение из проксируемого запроса»).
	res.Headers.Del("Authorization")
	return res, nil
}

// extractAndStripJSONField парсит JSON-объект, извлекает значение поля
// верхнего уровня (строка), удаляет его и возвращает сериализованный
// результат обратно. Если тело не JSON-объект — ошибка.
func extractAndStripJSONField(body []byte, field string) (token string, stripped []byte, err error) {
	if len(body) == 0 {
		return "", body, fmt.Errorf("body is empty")
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", body, fmt.Errorf("body is not JSON object")
	}
	raw, ok := obj[field]
	if !ok {
		return "", body, fmt.Errorf("field %s not present", field)
	}
	s, ok := raw.(string)
	if !ok {
		return "", body, fmt.Errorf("field %s is not a string", field)
	}
	delete(obj, field)
	out, err := json.Marshal(obj)
	if err != nil {
		return "", body, fmt.Errorf("re-marshal body: %w", err)
	}
	return s, out, nil
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		out[k] = append([]string(nil), vv...)
	}
	return out
}
