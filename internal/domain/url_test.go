package domain

import "testing"

func TestAbsoluteHTTPURL(t *testing.T) {
	t.Parallel()

	t.Run("валидные", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{
			"http://x.io",
			"https://api.partner.com/hook?a=1",
			"https://host:8443/path#frag",
			"HTTP://x.io", // схема регистронезависима, url.Parse её нормализует
		} {
			if _, ok := AbsoluteHTTPURL(raw); !ok {
				t.Errorf("%q: want ok", raw)
			}
		}
	})

	t.Run("невалидные", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{
			"vozovoz.lenobl.com/vozovoz/getstat", // боевой случай: хост без схемы
			"example.com",
			"//example.com", // протокол-относительный
			"/relative",
			"ftp://host/file",
			"https://", // пустой хост
			"javascript:alert(1)",
			"",
		} {
			if _, ok := AbsoluteHTTPURL(raw); ok {
				t.Errorf("%q: want !ok", raw)
			}
		}
	})

	// Пробелы НЕ нормализуются: вызывающие используют дальше собственную строку
	// (ResolveURL возвращает исходный url_base), поэтому «почистить» здесь значило
	// бы проверить одно, а отправить другое — ошибка всплыла бы уже на доставке.
	t.Run("пробелы делают адрес невалидным", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{" https://x.io/a", "\thttps://x.io"} {
			if _, ok := AbsoluteHTTPURL(raw); ok {
				t.Errorf("%q: want !ok (строка проверяется как есть)", raw)
			}
		}
	})
}
