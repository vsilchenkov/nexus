package domain

import (
	"net/url"
	"strings"
)

// AbsoluteHTTPURL парсит raw как абсолютный http(s)-URL и возвращает разобранное
// значение. ok=false, если строка не парсится, схема не http/https (в том числе
// пустая — «example.com/hook») или хост пуст.
//
// §69.2: единая точка проверки «это адрес, по которому можно сделать HTTP-запрос».
// Без схемы url.Parse ошибки не возвращает — кладёт всю строку в Path, поэтому
// проверять Scheme/Host обязательно: иначе битый адрес доезжает до Sender'а и
// падает там как `unsupported protocol scheme ""` (боевой инцидент 2026-07-27).
func AbsoluteHTTPURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, false
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	return u, true
}
