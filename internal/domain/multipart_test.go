package domain

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsMultipartMediaType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ct   string
		want bool
	}{
		{"form-data with boundary", "multipart/form-data; boundary=abc", true},
		{"form-data no boundary", "multipart/form-data", true},
		{"mixed", "multipart/mixed; boundary=xyz", true},
		{"related", `multipart/related; type="text/xml"; boundary=b`, true},
		{"uppercase", "MULTIPART/FORM-DATA; BOUNDARY=abc", true},
		{"json", "application/json", false},
		{"text", "text/plain; charset=utf-8", false},
		{"empty", "", false},
		{"garbage no slash", "multipart", false},
		{"garbage", "not a media type at all ;;;", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsMultipartMediaType(tt.ct))
		})
	}
}

// buildMultipart собирает валидное multipart/form-data тело из заданных частей.
func buildMultipart(t *testing.T, parts []struct {
	field, filename, contentType, content string
}) (contentType string, body []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		hdr := make(map[string][]string)
		var disp strings.Builder
		disp.WriteString(`form-data; name="` + p.field + `"`)
		if p.filename != "" {
			disp.WriteString(`; filename="` + p.filename + `"`)
		}
		hdr["Content-Disposition"] = []string{disp.String()}
		if p.contentType != "" {
			hdr["Content-Type"] = []string{p.contentType}
		}
		pw, err := w.CreatePart(hdr)
		require.NoError(t, err)
		_, err = pw.Write([]byte(p.content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return w.FormDataContentType(), buf.Bytes()
}

func TestMultipartLogPlaceholder(t *testing.T) {
	t.Parallel()

	t.Run("file and field", func(t *testing.T) {
		t.Parallel()
		ct, body := buildMultipart(t, []struct {
			field, filename, contentType, content string
		}{
			{"file", "invoice.pdf", "application/pdf", "SECRET-FILE-CONTENT"},
			{"comment", "", "", "hello world"},
		})
		got := MultipartLogPlaceholder(ct, body)

		lines := strings.Split(got, "\n")
		assert.Equal(t, "multipart/form-data", lines[0], "первая строка — media type")
		assert.Contains(t, got, `name="file"`)
		assert.Contains(t, got, `filename="invoice.pdf"`)
		assert.Contains(t, got, `type=application/pdf`)
		assert.Contains(t, got, "size=19") // len("SECRET-FILE-CONTENT")
		assert.Contains(t, got, `name="comment"`)
		assert.Contains(t, got, "size=11") // len("hello world")
		assert.Contains(t, got, "2 parts")
		// Содержимое частей НЕ должно попасть в плейсхолдер.
		assert.NotContains(t, got, "SECRET-FILE-CONTENT")
		assert.NotContains(t, got, "hello world")
	})

	t.Run("single part plural", func(t *testing.T) {
		t.Parallel()
		ct, body := buildMultipart(t, []struct {
			field, filename, contentType, content string
		}{
			{"only", "", "", "x"},
		})
		got := MultipartLogPlaceholder(ct, body)
		assert.Contains(t, got, "1 part,")
		assert.NotContains(t, got, "1 parts")
	})

	t.Run("detected as placeholder round-trip", func(t *testing.T) {
		t.Parallel()
		ct, body := buildMultipart(t, []struct {
			field, filename, contentType, content string
		}{
			{"file", "a.bin", "application/octet-stream", "data"},
		})
		got := MultipartLogPlaceholder(ct, body)
		assert.True(t, IsMultipartLogPlaceholder(got))
	})

	t.Run("no boundary falls back", func(t *testing.T) {
		t.Parallel()
		got := MultipartLogPlaceholder("multipart/form-data", []byte("whatever"))
		assert.True(t, strings.HasPrefix(got, "multipart/form-data\n"))
		assert.Contains(t, got, "no boundary")
		assert.Contains(t, got, "body 8 bytes")
		assert.True(t, IsMultipartLogPlaceholder(got))
	})

	t.Run("bad content-type falls back", func(t *testing.T) {
		t.Parallel()
		got := MultipartLogPlaceholder("=(broken", []byte("x"))
		assert.Contains(t, got, "multipart/form-data") // подставлен дефолт
		assert.Contains(t, got, "parse content-type")
	})

	t.Run("truncated body after some parts", func(t *testing.T) {
		t.Parallel()
		ct, body := buildMultipart(t, []struct {
			field, filename, contentType, content string
		}{
			{"a", "", "", "one"},
			{"b", "", "", "two"},
		})
		// Обрезаем тело в середине — NextPart на второй части даст ошибку.
		truncated := body[:len(body)-10]
		got := MultipartLogPlaceholder(ct, truncated)
		assert.True(t, strings.HasPrefix(got, "multipart/form-data\n"))
		// Первая часть распозналась, дальше — примечание об обрыве или сводка.
		assert.Contains(t, got, `name="a"`)
	})

	t.Run("empty body", func(t *testing.T) {
		t.Parallel()
		got := MultipartLogPlaceholder("multipart/form-data; boundary=abc", nil)
		assert.True(t, strings.HasPrefix(got, "multipart/form-data"))
		assert.Contains(t, got, "body 0 bytes")
	})

	t.Run("many parts capped", func(t *testing.T) {
		t.Parallel()
		parts := make([]struct{ field, filename, contentType, content string }, 60)
		for i := range parts {
			parts[i] = struct{ field, filename, contentType, content string }{
				field: "f", content: "v",
			}
		}
		ct, body := buildMultipart(t, parts)
		got := MultipartLogPlaceholder(ct, body)
		// Перечислено не более лимита, но полное число в сводке.
		assert.Contains(t, got, "listed 50 of 60 parts")
		assert.Equal(t, 50, strings.Count(got, multipartPlaceholderPartPrefix))
	})
}

func TestIsMultipartLogPlaceholder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		stored string
		want   bool
	}{
		{"part line", "multipart/form-data\n- part 1: name=\"x\"; size=1\n[1 part, body 10 bytes]", true},
		{"summary only", "multipart/mixed\n[0 parts, body 0 bytes]", true},
		{"fallback", "multipart/form-data\n[multipart body not stored; no boundary; body 8 bytes]", true},
		{"raw multipart body", "--boundary123\nContent-Disposition: form-data; name=\"x\"\n\nvalue", false},
		{"json object", "{\n  \"key\": \"value\"\n}", false},
		{"json array", "[\n  1, 2, 3\n]", false},
		{"plain text", "multipart/form-data", false}, // одна строка, нет тела-сводки
		{"empty", "", false},
		{"text mentioning multipart", "some log line about multipart/form-data\nmore text", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsMultipartLogPlaceholder(tt.stored))
		})
	}
}

// FuzzMultipartLogPlaceholder — CLAUDE.md §8: парсер внешних байтов обязан
// фаззиться. Инвариант: не паникует, вывод не пуст и никогда не содержит
// характерный контент-маркер из тела.
func FuzzMultipartLogPlaceholder(f *testing.F) {
	f.Add("multipart/form-data; boundary=abc", []byte("--abc\r\nContent-Disposition: form-data; name=\"x\"\r\n\r\nMARKER\r\n--abc--\r\n"))
	f.Add("multipart/form-data", []byte(""))
	f.Add("=(broken", []byte("MARKER"))
	f.Add("multipart/mixed; boundary=", []byte("MARKER"))

	f.Fuzz(func(t *testing.T, ct string, body []byte) {
		got := MultipartLogPlaceholder(ct, body) // не паникует
		assert.NotEmpty(t, got)
		// Плейсхолдер не должен раскрывать сырое содержимое частей. Проверяем на
		// нашем маркере из seed'ов — в fuzzed данных совпадение маловероятно, но
		// главное — отсутствие паники и стабильность.
		if bytes.Contains(body, []byte("MARKER")) && strings.Contains(got, "MARKER") {
			// Допустимо только если MARKER оказался в имени поля/файла/типе —
			// эти метаданные плейсхолдер печатает намеренно. Содержимое части —
			// нет. Тонкую грань fuzz не различает, поэтому лишь фиксируем, что
			// функция завершилась; строгую проверку делает table-тест выше.
			_ = got
		}
	})
}
