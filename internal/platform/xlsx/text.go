package xlsx

import "unicode/utf8"

// MaxCellChars — предел длины текста ячейки Excel в UTF-16 единицах.
// Больше Excel не хранит: файл с более длинной ячейкой он «восстанавливает»,
// обрезая её, и показывает пользователю предупреждение о повреждении.
const MaxCellChars = 32767

// UTF16Len — длина строки в UTF-16 единицах, как её считает Excel: символ вне
// базовой плоскости занимает две. Невалидный байт UTF-8 считается одним
// символом — в файле он станет U+FFFD.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}
	return n
}

// Truncate урезает s так, чтобы вместе с marker уложиться в maxUnits UTF-16
// единиц, не разрывая символ (суррогатную пару). Строка, которая и так
// помещается, возвращается как есть и без маркера. Второе значение — была ли
// обрезка.
func Truncate(s string, maxUnits int, marker string) (string, bool) {
	if UTF16Len(s) <= maxUnits {
		return s, false
	}
	budget := max(maxUnits-UTF16Len(marker), 0)
	n := 0
	for i, r := range s {
		u := runeUnits(r)
		if n+u > budget {
			return s[:i] + marker, true
		}
		n += u
	}
	return s + marker, true
}

func runeUnits(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// writeText пишет s как содержимое элемента <t> ячейки и возвращает true, если
// текст пришлось обрезать на maxUnits UTF-16 единиц.
//
// Экранируются только & < > — не encoding/xml.EscapeText: тот заменяет кавычки
// и переводы строк ссылками, и JSON-тело раздувалось бы в разы. Безопасные
// отрезки строки пишутся целиком, а не по символу: тела бывают мегабайтными.
func writeText(o *out, s string, maxUnits int) bool {
	units, start := 0, 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		u := runeUnits(r)
		if units+u > maxUnits {
			o.str(s[start:i])
			return true
		}
		units += u
		if rep, ok := replacement(r, size, s[i+size:]); ok {
			o.str(s[start:i])
			o.str(rep)
			start = i + size
		}
		i += size
	}
	o.str(s[start:])
	return false
}

// replacement — чем заменить символ в тексте ячейки; ok=false — писать как есть.
//
//   - \r — ссылкой: разборщик XML нормализует CRLF в LF, и тело изменилось бы;
//   - литеральный «_xHHHH_» — экранированием подчёркивания: Excel раскодирует
//     такую последовательность в символ, и тело показывалось бы искажённым;
//   - невалидный UTF-8 и символы вне XML 1.0 (управляющие) — U+FFFD: иначе
//     Excel отказывается открывать файл целиком.
func replacement(r rune, size int, rest string) (string, bool) {
	switch {
	case r == '&':
		return "&amp;", true
	case r == '<':
		return "&lt;", true
	case r == '>':
		return "&gt;", true
	case r == '\r':
		return "&#13;", true
	case r == '_' && escapeLike(rest):
		return "_x005F_", true
	case r == utf8.RuneError && size == 1, !xmlChar(r):
		return string(utf8.RuneError), true
	}
	return "", false
}

// escapeLike — rest начинается с «xHHHH_» (хвост экранирования Excel).
func escapeLike(rest string) bool {
	if len(rest) < 6 || rest[0] != 'x' || rest[5] != '_' {
		return false
	}
	for i := 1; i <= 4; i++ {
		if !isHex(rest[i]) {
			return false
		}
	}
	return true
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// xmlChar — символ допустим в XML 1.0 (production Char).
func xmlChar(r rune) bool {
	switch {
	case r == 0x9, r == 0xA, r == 0xD:
		return true
	case r >= 0x20 && r <= 0xD7FF:
		return true
	case r >= 0xE000 && r <= 0xFFFD:
		return true
	case r >= 0x10000 && r <= 0x10FFFF:
		return true
	}
	return false
}
