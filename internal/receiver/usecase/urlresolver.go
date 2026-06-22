package usecase

import (
	"fmt"
	"net/url"

	"nexus/internal/domain"
)

// ResolveURL вычисляет фактический URL отправки и возвращает его + query-string
// без служебных параметров шины (§3.4 ТЗ).
//
//	urlMode=static:       target = node.TargetURL; query as-is.
//	urlMode=from_request: target = значение query-параметра node.URLParamName;
//	                      сам параметр вырезан из query.
func ResolveURL(node *domain.Node, incomingQuery url.Values) (target string, cleanQuery url.Values, err error) {
	switch node.URLMode {
	case domain.URLModeStatic:
		return node.TargetURL, cloneValues(incomingQuery), nil

	case domain.URLModeFromRequest:
		param := node.URLParamName
		raw := incomingQuery.Get(param)
		if raw == "" {
			return "", nil, fmt.Errorf("%w: %s", domain.ErrURLParamRequired, param)
		}
		parsed, perr := url.Parse(raw)
		if perr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", nil, fmt.Errorf("%w: %s", domain.ErrURLInvalid, raw)
		}
		if !domain.HostAllowed(parsed.Host, node.URLAllowedHosts) {
			return "", nil, fmt.Errorf("%w: %s", domain.ErrURLNotAllowed, parsed.Host)
		}
		// Вырезаем служебный параметр.
		clean := cloneValues(incomingQuery)
		clean.Del(param)
		return raw, clean, nil

	default:
		return "", nil, fmt.Errorf("%w: %q", domain.ErrNodeInvalidURLMode, node.URLMode)
	}
}

// appendPathSuffix приклеивает remainder (хвост входящего пути, §39 path-passthrough)
// к целевому URL, сохраняя его query-часть. Использует (*url.URL).JoinPath:
// он корректно кодирует сегменты и резолвит «.»/«..» — клиент не может через
// «../» выйти за пределы базового пути target_url. Применяется и к static, и к
// from_request URL (хвост клеится к любому резолвнутому адресу).
func appendPathSuffix(target, remainder string) string {
	if remainder == "" {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	return u.JoinPath(remainder).String()
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vv := range v {
		out[k] = append([]string(nil), vv...)
	}
	return out
}
