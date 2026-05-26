package usecase

import (
	"fmt"
	"net/url"
	"strings"

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
		if !hostAllowed(parsed.Host, node.URLAllowedHosts) {
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

// hostAllowed: пустой allowlist = разрешено всё.
// Поддерживаются wildcard-паттерны вида "*.partner.com".
func hostAllowed(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	low := strings.ToLower(host)
	// Срезаем порт, если есть.
	if i := strings.Index(low, ":"); i >= 0 {
		low = low[:i]
	}
	for _, pat := range allowlist {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if pat == "" {
			continue
		}
		if pat == low {
			return true
		}
		if strings.HasPrefix(pat, "*.") {
			suffix := pat[1:] // ".partner.com"
			if strings.HasSuffix(low, suffix) && low != suffix[1:] {
				return true
			}
		}
	}
	return false
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vv := range v {
		out[k] = append([]string(nil), vv...)
	}
	return out
}
