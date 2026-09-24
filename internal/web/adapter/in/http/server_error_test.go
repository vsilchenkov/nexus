package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// bufferLogger — логгер, пишущий JSON-строки в буфер: по нему видно, на каком
// уровне ушла строка (ERROR уходит в Sentry).
func bufferLogger(buf *bytes.Buffer) logging.Logger {
	return logging.NewLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// TestLogServerError — §109.4 (Sentry NEXUS-11/13): отмена запроса, который
// оборвал сам клиент, уходит на Debug; всё остальное — прежний ERROR.
func TestLogServerError(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	scanCanceled := fmt.Errorf("scan node: %w", context.Canceled)
	cases := []struct {
		name       string
		clientGone bool
		err        error
		wantLevel  string
	}{
		{"клиент ушёл, отмена → DEBUG", true, scanCanceled, "DEBUG"},
		{"клиент ушёл, другая ошибка → ERROR", true, errors.New("connection refused"), "ERROR"},
		{"отмена при живом запросе → ERROR", false, scanCanceled, "ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.clientGone {
				cancel()
			}
			c.Request = httptest.NewRequest(http.MethodGet, "/api/nodes/n1/logs", nil).WithContext(ctx)

			var buf bytes.Buffer
			logServerError(c, bufferLogger(&buf), "logs list failed", tc.err, "logs.list")

			assert.Contains(t, buf.String(), `"level":"`+tc.wantLevel+`"`, buf.String())
			assert.Contains(t, buf.String(), "logs.list", "op сохраняется на любом уровне")
		})
	}
}

// TestReceiverProxy_ClientGone — §109.4: клиент оборвал запрос, пока Receiver
// думал. Прокси отвечает 499 (метрики и спан Sentry видят уход клиента, а не
// 502) и пишет Debug вместо ERROR «receiver proxy upstream error».
func TestReceiverProxy_ClientGone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // Receiver «думает», пока клиент не уйдёт
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	status := make(chan int, 1)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Next()
		status <- c.Writer.Status()
	})
	var buf bytes.Buffer
	RegisterReceiverProxy(r, upstream.URL, bufferLogger(&buf))
	web := httptest.NewServer(r)
	defer web.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	// Путь passthrough-узла с токеном бота: в строке лога его быть не должно.
	const secret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw_"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		web.URL+"/api/v1/request/telegram/bot1:"+secret+"/getUpdates", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, err, context.Canceled)

	select {
	case got := <-status:
		assert.Equal(t, statusClientClosedRequest, got)
	case <-time.After(5 * time.Second):
		t.Fatal("прокси не завершил обработку после ухода клиента")
	}
	assert.False(t, strings.Contains(buf.String(), `"level":"ERROR"`), buf.String())
	assert.Contains(t, buf.String(), "canceled by client")
	assert.NotContains(t, buf.String(), secret)
	assert.Contains(t, buf.String(), "bot1:***")
}
