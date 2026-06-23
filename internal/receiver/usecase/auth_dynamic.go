package usecase

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"nexus/internal/domain"
)

// DynamicAuthResult — результат разбора динамической авторизации (§3.5, §41).
//
//	Header — готовая строка для outbound-заголовка Authorization
//	         ("Bearer <token>" / "Basic <base64>"). Пустая строка означает
//	         «не пробрасывать Authorization» (§41: поле не пришло/пустое).
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

// BuildDynamicOutgoingAuth обрабатывает оба динамических режима
// (token_from_request, basic_from_request). Для статических режимов
// (none/basic/token) возвращает ошибку — вызывающий должен использовать
// BuildOutgoingAuth. Ошибка возвращается только при конфигурационной проблеме
// (невалидный источник); отсутствие/пустота значения ошибкой НЕ является
// (§41: тогда Header=="" и запрос уходит без Authorization).
func BuildDynamicOutgoingAuth(
	node *domain.Node,
	in http.Header,
	q url.Values,
	body []byte,
) (*DynamicAuthResult, error) {
	switch node.AuthType {
	case domain.AuthTypeTokenFromRequest:
		return buildFromRequest(node, in, q, body, "Bearer")
	case domain.AuthTypeBasicFromRequest:
		return buildFromRequest(node, in, q, body, "Basic")
	default:
		return nil, fmt.Errorf("%w: %q is not dynamic", domain.ErrNodeInvalidAuthType, node.AuthType)
	}
}

// buildFromRequest извлекает креду из источника node.AuthDynamicSource по имени
// node.AuthDynamicField, вырезает её из проксируемого запроса (§3.5) и формирует
// Authorization-заголовок со схемой scheme ("Bearer"/"Basic"). §41:
//   - пустое/отсутствующее значение → Header=="" (запрос уходит без авторизации);
//   - умный дедуп схемы: если значение уже начинается с "<scheme> " — берём как
//     есть (исключает "Bearer Bearer ..." для кейса ?Bearer=Bearer+<jwt>).
func buildFromRequest(
	node *domain.Node,
	in http.Header,
	q url.Values,
	body []byte,
	scheme string,
) (*DynamicAuthResult, error) {
	value, res, err := extractDynamicValue(node, in, q, body)
	if err != nil {
		return nil, err
	}
	if value == "" {
		// §41: нечего пробрасывать — отдаём очищенный запрос без Authorization.
		res.Header = ""
		return res, nil
	}
	res.Header = withScheme(scheme, value)
	return res, nil
}

// extractDynamicValue достаёт сырое значение креды из настроенного источника и
// удаляет его из копий query/headers/body. Возвращает пустую строку, если поле
// не сконфигурировано, не пришло или пустое (вызывающий трактует это как
// «без авторизации»). Ошибка — только при невалидном источнике (конфиг).
func extractDynamicValue(
	node *domain.Node,
	in http.Header,
	q url.Values,
	body []byte,
) (string, *DynamicAuthResult, error) {
	res := &DynamicAuthResult{
		Query:   cloneValues(q),
		Headers: cloneHeader(in),
		Body:    body,
	}
	field := node.AuthDynamicField
	if field == "" {
		return "", res, nil
	}

	var value string
	switch node.AuthDynamicSource {
	case domain.AuthDynSourceQuery:
		value = res.Query.Get(field)
		res.Query.Del(field)
	case domain.AuthDynSourceHeader:
		value = res.Headers.Get(field)
		res.Headers.Del(field)
		// Back-compat: ручной strip-prefix (если задан) применяется до умного
		// дедупа схемы. Для большинства кейсов дедуп делает его ненужным.
		if prefix := node.AuthDynamicStripPrefix; prefix != "" {
			value = strings.TrimPrefix(value, prefix)
		}
	case domain.AuthDynSourceBody:
		// §41: поля нет / тело не JSON-объект → token=="" (нет значения), тело
		// остаётся нетронутым (extractAndStripJSONField вернул исходное). Ошибка
		// здесь не фатальна: пустое значение трактуется как «без авторизации».
		token, stripped, _ := extractAndStripJSONField(body, field)
		value = token
		res.Body = stripped
	default:
		return "", res, fmt.Errorf("%w: %q", domain.ErrNodeInvalidAuthDynSource, node.AuthDynamicSource)
	}
	return strings.TrimSpace(value), res, nil
}

// withScheme возвращает значение Authorization со схемой scheme. Если value уже
// начинается со "<scheme> " (регистронезависимо) — возвращается как есть (§41
// п.2: умный дедуп, исключает удвоение "Bearer Bearer <jwt>").
func withScheme(scheme, value string) string {
	if strings.HasPrefix(strings.ToLower(value), strings.ToLower(scheme)+" ") {
		return value
	}
	return scheme + " " + value
}

// extractAndStripJSONField парсит JSON-объект, извлекает значение поля
// верхнего уровня (строка), удаляет его и возвращает сериализованный
// результат обратно. Если тело не JSON-объект или поля нет — возвращает
// исходное тело без изменений и ошибку (вызывающий трактует как «нет значения»).
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
