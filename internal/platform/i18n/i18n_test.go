package i18n

import "testing"

func TestParseAcceptLanguage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header string
		want   Lang
	}{
		{"", LangEN},
		{"en", LangEN},
		{"ru", LangRU},
		{"ru-RU", LangRU},
		{"en-US,en;q=0.9", LangEN},
		{"ru-RU,en;q=0.5", LangRU},
		{"fr,de", LangEN}, // ничего не распознано → default
		{"ru;q=1.0,en;q=0.5", LangRU},
	}
	for _, tc := range cases {
		if got := ParseAcceptLanguage(tc.header); got != tc.want {
			t.Errorf("ParseAcceptLanguage(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestTranslate(t *testing.T) {
	t.Parallel()
	if got := Translate(LangRU, "auth.invalid_credentials"); got != "неверный логин или пароль" {
		t.Errorf("ru auth.invalid_credentials: %q", got)
	}
	if got := Translate(LangEN, "auth.invalid_credentials"); got != "invalid credentials" {
		t.Errorf("en auth.invalid_credentials: %q", got)
	}
	// Fallback на en, если в ru нет ключа.
	if got := Translate(LangRU, "node.not_found"); got != "узел не найден" {
		t.Errorf("ru node.not_found: %q", got)
	}
	// Несуществующий ключ → сам ключ.
	if got := Translate(LangEN, "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("missing key fallback: %q", got)
	}
}
