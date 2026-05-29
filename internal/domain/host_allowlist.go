package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HostKind — тип паттерна разрешённого хоста (§23 ТЗ).
//   - exact    — точный hostname (RFC 1123), напр. api.partner.com;
//   - wildcard — *.<hostname>, матчит любой поддомен одного уровня;
//   - regex    — Go-регексп (regexp.Compile), применяется к hostname без порта.
type HostKind string

const (
	HostKindExact    HostKind = "exact"
	HostKindWildcard HostKind = "wildcard"
	HostKindRegex    HostKind = "regex"
)

// Valid сообщает, входит ли значение в множество допустимых типов.
func (k HostKind) Valid() bool {
	switch k {
	case HostKindExact, HostKindWildcard, HostKindRegex:
		return true
	default:
		return false
	}
}

// RegexEncodePrefix — префикс, которым regex-паттерн кодируется в плоский
// денормализованный снимок nodes.url_allowed_hosts (TEXT[]). Безопасен:
// валидный hostname не может содержать ':' и потому не начинается с "re:".
// Receiver-матчер (HostAllowed) распознаёт этот префикс. См. §23.
const RegexEncodePrefix = "re:"

// HostAllowlistEntry — запись каталога разрешённых хостов (§23 ТЗ).
// Каталог общий для инсталляции (не per-team) и переиспользуется между узлами
// через таблицу node_allowed_hosts. usage_count денормализован (trigger).
type HostAllowlistEntry struct {
	ID          string
	Pattern     string
	Kind        HostKind
	Description string
	UsageCount  int
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// hostLabel — одна метка hostname по RFC 1123: 1–63 символа, без ведущего/
// замыкающего дефиса.
const hostLabel = `[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?`

// hostnamePattern — полный hostname: метки через точку, без двойных точек,
// без ведущей/замыкающей точки.
var hostnamePattern = regexp.MustCompile(`^` + hostLabel + `(?:\.` + hostLabel + `)*$`)

// Validate проверяет инварианты записи каталога. Constraints в БД — второй
// уровень защиты; здесь — основной (user-friendly ошибка до похода в БД).
func (e *HostAllowlistEntry) Validate() error {
	if !e.Kind.Valid() {
		return ErrHostInvalidKind
	}
	if l := len(e.Pattern); l < 1 || l > 512 {
		return ErrHostPatternLength
	}
	if len(e.Description) > 500 {
		return ErrHostDescriptionLength
	}
	switch e.Kind {
	case HostKindExact:
		if len(e.Pattern) > 253 || !hostnamePattern.MatchString(e.Pattern) {
			return ErrHostExactFormat
		}
	case HostKindWildcard:
		rest, ok := strings.CutPrefix(e.Pattern, "*.")
		if !ok || len(rest) > 253 || !hostnamePattern.MatchString(rest) {
			return ErrHostWildcardFormat
		}
	case HostKindRegex:
		if _, err := regexp.Compile(e.Pattern); err != nil {
			return fmt.Errorf("%w: %v", ErrHostRegexInvalid, err)
		}
	}
	return nil
}

// HostAllowed сообщает, разрешён ли host списком паттернов из денормализованного
// снимка nodes.url_allowed_hosts. Единый матчер для Receiver (горячий путь) и
// Web-preview (§23, DRY).
//
//   - пустой список = разрешено всё (как было до каталога);
//   - host нормализуется: lower-case, порт срезается;
//   - exact: точное совпадение (без учёта регистра);
//   - "*.x": любой поддомен x (но не сам x);
//   - "re:<regexp>": Go-регексп против hostname; невалидный regexp = deny.
//
// Regex-паттерн НЕ приводится к нижнему регистру (это сломало бы классы вроде
// \D); хост уже lower-case, чего достаточно для регистронезависимых hostname.
func HostAllowed(host string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	low := strings.ToLower(host)
	if i := strings.Index(low, ":"); i >= 0 {
		low = low[:i]
	}
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if rx, ok := strings.CutPrefix(pat, RegexEncodePrefix); ok {
			re, err := regexp.Compile(rx)
			if err != nil {
				continue // невалидный regexp = deny
			}
			if re.MatchString(low) {
				return true
			}
			continue
		}
		lp := strings.ToLower(pat)
		if lp == low {
			return true
		}
		if suffix, ok := strings.CutPrefix(lp, "*."); ok {
			if strings.HasSuffix(low, "."+suffix) {
				return true
			}
		}
	}
	return false
}

// EncodedPattern возвращает форму паттерна для денормализованного снимка
// nodes.url_allowed_hosts. exact/wildcard сохраняются как есть, regex — с
// префиксом RegexEncodePrefix, чтобы Receiver-матчер отличил его.
func (e *HostAllowlistEntry) EncodedPattern() string {
	if e.Kind == HostKindRegex {
		return RegexEncodePrefix + e.Pattern
	}
	return e.Pattern
}
