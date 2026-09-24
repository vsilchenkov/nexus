package sentry

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// botSecret — секрет в формате настоящего токена Telegram.
const botSecret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw_"

const botPath = "telegram/bot123456789:" + botSecret + "/getUpdates"

// tokenEverywhere — событие, в котором токен бота лежит во всех полях, куда он
// попадал или мог попасть: путь узла в теге node (утечка до §101.13), текст
// ошибки, URL запроса, имя транзакции несматченного маршрута, slog-контекст,
// хлебная крошка и спан.
func tokenEverywhere() *sentry.Event {
	return &sentry.Event{
		Type:        "transaction",
		Message:     "sender.Send: " + botPath,
		Transaction: "/api/v1/" + botPath,
		Tags:        map[string]string{"node": botPath},
		Exception:   []sentry.Exception{{Value: `Post "https://api.telegram.org/bot123456789:` + botSecret + `/x": EOF`}},
		Request:     &sentry.Request{URL: "https://nexus.example/api/v1/" + botPath},
		Contexts: map[string]sentry.Context{
			"log":   {"node": botPath},
			"trace": {"data": map[string]any{"http.url": botPath}},
		},
		Breadcrumbs: []*sentry.Breadcrumb{{Message: botPath, Data: map[string]any{"path": botPath}}},
		Spans: []*sentry.Span{{
			Name:        botPath,
			Description: "POST " + botPath,
			Tags:        map[string]string{"node": botPath},
			Data:        map[string]any{"url": botPath},
		}},
	}
}

// TestBeforeSendTransaction_MasksBotTokens — §109.6: секрет токена бота не
// уходит в Sentry ни в одном поле, независимо от имени поля; `bot<id>:`
// остаётся — по нему видно, какой бот. Проверяется итоговый JSON — ровно то,
// что SDK отправит.
func TestBeforeSendTransaction_MasksBotTokens(t *testing.T) {
	t.Parallel()

	out := beforeSendTransaction(tokenEverywhere(), nil)
	require.NotNil(t, out)
	raw, err := json.Marshal(out)
	require.NoError(t, err)

	assert.NotContains(t, string(raw), botSecret)
	assert.Contains(t, string(raw), "bot123456789:***")
	assert.Equal(t, "telegram/bot123456789:***/getUpdates", out.Tags["node"])
}

// TestBeforeSend_MasksBotTokens — то же для события-ошибки: у него свой хук.
func TestBeforeSend_MasksBotTokens(t *testing.T) {
	t.Parallel()

	ev := tokenEverywhere()
	ev.Type = ""
	ev.Spans = nil
	out := beforeSend(ev, &sentry.EventHint{OriginalException: errors.New(botPath)})
	require.NotNil(t, out)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), botSecret)
}

func TestBeforeBreadcrumb_MasksBotTokens(t *testing.T) {
	t.Parallel()

	b := beforeBreadcrumb(&sentry.Breadcrumb{Message: botPath, Data: map[string]any{"path": botPath}}, nil)
	require.NotNil(t, b)
	assert.Equal(t, "telegram/bot123456789:***/getUpdates", b.Message)
	assert.Equal(t, "telegram/bot123456789:***/getUpdates", b.Data["path"])
}
