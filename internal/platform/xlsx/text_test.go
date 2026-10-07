package xlsx

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fffd — U+FFFD, которым заменяются недопустимые символы.
const fffd = string(utf8.RuneError)

// renderText — то, что writeText кладёт внутрь <t>.
func renderText(s string, maxUnits int) (string, bool) {
	var buf bytes.Buffer
	o := &out{bw: bufio.NewWriter(&buf)}
	cut := writeText(o, s, maxUnits)
	if err := o.flush(); err != nil {
		panic(err)
	}
	return buf.String(), cut
}

// decodeT — текст ячейки так, как его прочтёт XML-разборщик Excel (без
// раскодирования _xHHHH_ — оно проверяется отдельно).
func decodeT(t *testing.T, raw string) string {
	t.Helper()
	var v struct {
		T string `xml:",chardata"`
	}
	require.NoError(t, xml.Unmarshal([]byte(`<t xml:space="preserve">`+raw+`</t>`), &v))
	return v.T
}

func TestWriteText_Escaping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantRaw string
	}{
		{"markup chars", `a&b<c>d`, `a&amp;b&lt;c&gt;d`},
		{"quotes and newlines kept as is", "{\"k\":\"v\"}\n\t", "{\"k\":\"v\"}\n\t"},
		{"carriage return as reference", "a\r\nb", "a&#13;\nb"},
		{"excel escape sequence", "_x000D_ и _x00e9_", "_x005F_x000D_ и _x005F_x00e9_"},
		{"not an escape", "_x00G0_ _x12_ snake_case", "_x00G0_ _x12_ snake_case"},
		{"control char", "a\x01b\x1fc", "a" + fffd + "b" + fffd + "c"},
		{"invalid utf8", "a\xffb", "a" + fffd + "b"},
		{"non-characters", "a" + string(rune(0xFFFE)) + "b", "a" + fffd + "b"},
		{"cyrillic and emoji", "Привет 😀", "Привет 😀"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, cut := renderText(tt.in, MaxCellChars)
			assert.False(t, cut)
			assert.Equal(t, tt.wantRaw, raw)
		})
	}
}

// Текст, прочитанный XML-разборщиком, совпадает с исходным: CR не теряется,
// кавычки и разметка не искажены.
func TestWriteText_RoundTrip(t *testing.T) {
	t.Parallel()
	in := "{\"a\": \"<b>&amp;</b>\"}\r\nстрока 2\tконец"
	raw, _ := renderText(in, MaxCellChars)
	assert.Equal(t, in, decodeT(t, raw))
}

func TestWriteText_CutKeepsSurrogatePairs(t *testing.T) {
	t.Parallel()
	// 4 символа «a» + эмодзи (2 единицы UTF-16): на пределе 5 эмодзи не влезает
	// целиком и не должен разрываться.
	raw, cut := renderText("aaaa😀b", 5)
	assert.True(t, cut)
	assert.Equal(t, "aaaa", raw)

	raw, cut = renderText("aaa😀b", 5)
	assert.True(t, cut)
	assert.Equal(t, "aaa😀", raw)

	raw, cut = renderText("aaa😀", 5)
	assert.False(t, cut)
	assert.Equal(t, "aaa😀", raw)
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	const marker = "…(cut)"
	tests := []struct {
		name    string
		in      string
		max     int
		want    string
		wantCut bool
	}{
		{"fits", "hello", 5, "hello", false},
		{"ascii", "hello world", 9, "hel…(cut)", true},
		{"surrogate at boundary", "ab😀cdefgh", 9, "ab…(cut)", true},
		{"marker longer than limit", "abcdef", 3, "…(cut)", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, cut := Truncate(tt.in, tt.max, marker)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantCut, cut)
		})
	}
	got, cut := Truncate(strings.Repeat("я", MaxCellChars+10), MaxCellChars, marker)
	assert.True(t, cut)
	assert.Equal(t, MaxCellChars, UTF16Len(got))
}

func TestUTF16Len(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, UTF16Len(""))
	assert.Equal(t, 6, UTF16Len("привет"))
	assert.Equal(t, 3, UTF16Len("a😀"))
	assert.Equal(t, 1, UTF16Len("\xff"))
}

// FuzzWriteText — любой вход даёт разборчивый XML, укладывается в предел и
// без обрезки читается обратно в исходный текст (с поправкой на замены).
func FuzzWriteText(f *testing.F) {
	for _, s := range []string{"", "a&b", "_x000D_", "\r\n", "\x00\xff", "😀😀", "_x005F_x0041_"} {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, maxUnits int) {
		maxUnits = max(maxUnits%64, 0)
		raw, cut := renderText(s, maxUnits)
		got := decodeT(t, raw)
		unescaped := strings.ReplaceAll(got, "_x005F_", "_")
		require.True(t, utf8.ValidString(got))
		require.LessOrEqual(t, UTF16Len(unescaped), maxUnits)
		if !cut && utf8.ValidString(s) && allXMLChars(s) {
			require.Equal(t, s, unescaped)
		}
	})
}

func allXMLChars(s string) bool {
	for _, r := range s {
		if !xmlChar(r) {
			return false
		}
	}
	return true
}
