package sensitive

import "strings"

// Masked — то, чем заменяется значение секрета.
const Masked = "***"

// minBotSecretLen — нижняя граница длины секрета токена бота. У настоящих
// токенов Telegram секрет — 35 символов; короткий хвост после `bot<id>:` —
// скорее обычный путь, и маскировать его незачем.
const minBotSecretLen = 20

// MaskBotTokens заменяет секрет токена Telegram-бота (`bot<id>:<secret>`) на
// Masked, оставляя `bot<id>:` — по нему видно, какой это бот, но не его ключ.
//
// Ищет по ЗНАЧЕНИЮ, а не по имени поля (§109.6): токен приходит в пути URL
// passthrough-узла, и ни тег node, ни message, ни имя транзакции о нём ничего
// не говорят — маскирование по имени ключа (IsSensitive) его не видит.
// Строка без токена возвращается как есть, без аллокаций.
func MaskBotTokens(s string) string {
	start, end, ok := nextBotSecret(s, 0)
	if !ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for ok {
		b.WriteString(s[last:start])
		b.WriteString(Masked)
		last = end
		start, end, ok = nextBotSecret(s, end)
	}
	b.WriteString(s[last:])
	return b.String()
}

// nextBotSecret ищет в s, начиная с from, секрет токена бота и возвращает его
// границы [start, end). Префикс `bot` — без учёта регистра, id — цифры,
// секрет — символы base64url не короче minBotSecretLen.
func nextBotSecret(s string, from int) (start, end int, ok bool) {
	for i := from; i+len("bot") < len(s); i++ {
		if !hasBotPrefixAt(s, i) {
			continue
		}
		j := i + len("bot")
		k := j
		for k < len(s) && isDigit(s[k]) {
			k++
		}
		if k == j || k >= len(s) || s[k] != ':' {
			continue
		}
		end = k + 1
		for end < len(s) && isSecretByte(s[end]) {
			end++
		}
		if end-(k+1) >= minBotSecretLen {
			return k + 1, end, true
		}
	}
	return 0, 0, false
}

// hasBotPrefixAt: `bot` в любом регистре. `|0x20` приводит ASCII-букву к
// нижнему регистру; к 'b'/'o'/'t' так приводятся только сами эти буквы.
func hasBotPrefixAt(s string, i int) bool {
	return s[i]|0x20 == 'b' && s[i+1]|0x20 == 'o' && s[i+2]|0x20 == 't'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isSecretByte — алфавит секрета токена: base64url.
func isSecretByte(c byte) bool {
	return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z') || c == '_' || c == '-'
}
