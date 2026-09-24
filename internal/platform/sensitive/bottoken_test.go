package sensitive_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/sensitive"
)

// secret — секрет в формате настоящего токена Telegram (35 символов base64url).
const secret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw_"

func TestMaskBotTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "путь passthrough-узла", in: "telegram/bot123456789:" + secret + "/getUpdates", want: "telegram/bot123456789:***/getUpdates"},
		{name: "URL целиком", in: "https://api.telegram.org/bot42:" + secret + "/sendMessage?chat_id=1", want: "https://api.telegram.org/bot42:***/sendMessage?chat_id=1"},
		{name: "два токена", in: "bot1:" + secret + " и bot2:" + secret, want: "bot1:*** и bot2:***"},
		{name: "регистр префикса", in: "BOT7:" + secret, want: "BOT7:***"},
		{name: "текст ошибки", in: `Post "https://api.telegram.org/bot5:` + secret + `/x": EOF`, want: `Post "https://api.telegram.org/bot5:***/x": EOF`},
		{name: "короткий хвост — не токен", in: "bot1:abc/getMe", want: "bot1:abc/getMe"},
		{name: "без id", in: "bot:" + secret, want: "bot:" + secret},
		{name: "без двоеточия", in: "bot123/" + secret, want: "bot123/" + secret},
		{name: "обычный путь", in: "vika/telephony", want: "vika/telephony"},
		{name: "пусто", in: "", want: ""},
		{name: "обрыв на префиксе", in: "xbo", want: "xbo"},
		{name: "обрыв после двоеточия", in: "bot1:", want: "bot1:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, sensitive.MaskBotTokens(tc.in))
		})
	}
}

// FuzzMaskBotTokens — на любом входе без паники, идемпотентно, и токен,
// вставленный между любыми строками, замаскирован.
func FuzzMaskBotTokens(f *testing.F) {
	f.Add("telegram/bot123:"+secret+"/getUpdates", "x")
	f.Add("", "")
	f.Add("bot", "\xce\xf0")
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		once := sensitive.MaskBotTokens(prefix + suffix)
		assert.Equal(t, once, sensitive.MaskBotTokens(once), "идемпотентность")

		// «/» не входит в алфавит секрета: токен не склеится с соседями.
		in := prefix + "/bot99:" + secret + "/" + suffix
		assert.Contains(t, sensitive.MaskBotTokens(in), "/bot99:"+sensitive.Masked+"/")
	})
}

func BenchmarkMaskBotTokens_NoToken(b *testing.B) {
	s := strings.Repeat("vika/telephony/calls?from=1&to=2 ", 32)
	for b.Loop() {
		_ = sensitive.MaskBotTokens(s)
	}
}
