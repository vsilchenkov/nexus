package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/metrics"
)

// ackSign — HMAC-подпись тела для callback-узла (§16).
func ackSign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

// ackSpecFor — шаблон боевой формы §83 с заданной политикой деградации.
func ackSpecFor(onError ackspec.OnError) *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     onError,
	}
}

func ackNodes(t *testing.T, spec *ackspec.Spec) map[string]*domain.Node {
	t.Helper()
	node := shortURLNode("acs_sigur", domain.RootMethodRequestAsync)
	node.AsyncAck = spec
	return map[string]*domain.Node{"webhook|acs_sigur": node}
}

func postAck(t *testing.T, r http.Handler, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestAck_SigurEndToEnd — сквозной боевой сценарий: СКУД шлёт пакет журнала и
// получает ровно то эхо, которого ждёт, вместо {"result":true,"id":…}.
func TestAck_SigurEndToEnd(t *testing.T) {
	r, queue := newShortURLHandler(t, ackNodes(t, ackSpecFor(ackspec.OnErrorDefault)), nil)

	w := postAck(t, r, "/api/v1/requestAsync/webhook/acs_sigur",
		`{"logs":[{"logId":79154,"empId":"000004627","keyHex":"7577E3"}]}`)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, `{"confirmedLogId": 79154}`, w.Body.String())
	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	// Корреляция: в кастомном теле id шины может не быть вовсе.
	assert.NotEmpty(t, w.Header().Get("X-Nexus-Id"), "ответ обязан нести id сообщения")
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, 1, queue.published, "сообщение обязано уйти в очередь")
}

// TestAck_ShortURLAndCallback — шаблон применяется на всех входах приёма:
// короткая форма §78.1 и callback §16 идут через ту же ветку.
func TestAck_ShortURLAndCallback(t *testing.T) {
	t.Run("short url", func(t *testing.T) {
		r, queue := newShortURLHandler(t, ackNodes(t, ackSpecFor(ackspec.OnErrorDefault)), nil)

		w := postAck(t, r, "/api/v1/webhook/acs_sigur", `{"logs":[{"logId":7}]}`)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, `{"confirmedLogId": 7}`, w.Body.String())
		assert.Equal(t, 1, queue.published)
	})

	t.Run("callback with signature", func(t *testing.T) {
		node := shortURLNode("cb", domain.RootMethodRequestAsync)
		node.AsyncAck = ackSpecFor(ackspec.OnErrorDefault)
		node.IncomingAuthType = domain.IncomingAuthTypeWebhookSignature
		node.IncomingAuthCredentials = "s3cret"
		node.WebhookSignatureHeader = "X-Sig"
		nodes := map[string]*domain.Node{"webhook|cb": node}
		r, _ := newShortURLHandler(t, nodes, nil)

		body := `{"logs":[{"logId":11}]}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/callback/webhook/cb", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Sig", ackSign("s3cret", body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, `{"confirmedLogId": 11}`, w.Body.String())
	})
}

// TestAck_PausedNodeAnswersByTemplate — §3.6: пауза узла НЕ возвращает клиента
// к {queued:true}. Иначе постановка узла на паузу тихо вернула бы дубли.
func TestAck_PausedNodeAnswersByTemplate(t *testing.T) {
	nodes := ackNodes(t, ackSpecFor(ackspec.OnErrorDefault))
	nodes["webhook|acs_sigur"].Status = domain.NodeStatusPaused
	r, queue := newShortURLHandler(t, nodes, nil)

	w := postAck(t, r, "/api/v1/requestAsync/webhook/acs_sigur", `{"logs":[{"logId":3}]}`)

	require.Equal(t, http.StatusAccepted, w.Code, "код 202 остаётся — семантика §3.6 не меняется")
	assert.Equal(t, `{"confirmedLogId": 3}`, w.Body.String())
	assert.Equal(t, 1, queue.published)
}

// TestAck_DegradeFallsBackAndCounts — политика default: запрос принят, ответ
// прежний, метрика выросла. Без метрики деградация не видна вовсе: клиент
// получает 200, как и при успехе.
func TestAck_DegradeFallsBackAndCounts(t *testing.T) {
	m := metrics.New("receiver-test")
	r, queue := newShortURLHandler(t, ackNodes(t, ackSpecFor(ackspec.OnErrorDefault)), m)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/requestAsync/webhook/acs_sigur",
		strings.NewReader("--b\r\n"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=b")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["result"], "деградация возвращает штатный ответ")
	assert.NotEmpty(t, body["id"])
	assert.Equal(t, 1, queue.published, "запрос принят несмотря на провал шаблона")

	got := testutil.ToFloat64(m.AckRenderFailedTotal.WithLabelValues("acs_sigur", "body_not_json"))
	assert.Equal(t, 1.0, got, "деградация обязана быть видна в метрике")
}

// TestAck_FailureDoesNotDegradeNode — провал шаблона НЕ делает узел
// «деградировавшим». Исход узла (§52, nexus_node_last_request_error) и
// nexus_request_incomplete_total — про доставку ПРИЁМНИКУ и живут на стороне
// Sender'а; ack — это ответ самой шины. Иначе узел с кривым шаблоном светился
// бы красным в интерфейсе и в алертах, хотя доставка идёт нормально.
func TestAck_FailureDoesNotDegradeNode(t *testing.T) {
	cases := []struct {
		name    string
		onError ackspec.OnError
	}{
		{"default policy", ackspec.OnErrorDefault},
		{"error policy", ackspec.OnErrorError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := metrics.New("receiver-test")
			r, _ := newShortURLHandler(t, ackNodes(t, ackSpecFor(tc.onError)), m)

			postAck(t, r, "/api/v1/requestAsync/webhook/acs_sigur", `{"nothing":1}`)

			assert.Zero(t, testutil.ToFloat64(m.NodeLastRequestError.WithLabelValues("acs_sigur")),
				"ack не участвует в исходе узла (§52) — это ответ шины, а не приёмника")
			assert.Zero(t, testutil.ToFloat64(
				m.RequestsIncompleteTotal.WithLabelValues("requestAsync", "acs_sigur")),
				"счётчик неуспешных доставок принадлежит Sender'у и расти здесь не должен")
		})
	}
}

// TestAck_ErrorPolicyRejects — политика error: 400 и сообщение НЕ принято.
func TestAck_ErrorPolicyRejects(t *testing.T) {
	r, queue := newShortURLHandler(t, ackNodes(t, ackSpecFor(ackspec.OnErrorError)), nil)

	w := postAck(t, r, "/api/v1/requestAsync/webhook/acs_sigur", `{"nothing":1}`)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["result"])
	assert.Equal(t, "ack template render failed", body["message"])
	assert.Equal(t, 0, queue.published,
		"400 обязан означать «не принято» — иначе клиент повторит уже принятый пакет")
}

// TestAck_NoSpecKeepsLegacyResponse — узел без шаблона отвечает ровно как до
// §83. Дублирует существующие тесты умышленно: это граница обратной
// совместимости, и ломать её нельзя даже случайно.
func TestAck_NoSpecKeepsLegacyResponse(t *testing.T) {
	r, queue := newShortURLHandler(t, ackNodes(t, nil), nil)

	w := postAck(t, r, "/api/v1/requestAsync/webhook/acs_sigur", `{"logs":[{"logId":1}]}`)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, true, body["result"])
	assert.NotEmpty(t, body["id"])
	assert.Empty(t, w.Header().Get("X-Nexus-Id"), "прежний ответ новых заголовков не получает")
	assert.Equal(t, 1, queue.published)
}

// TestAck_ErrorsAreNeverTemplated — ошибки приёма отвечают своей формой, а не
// шаблоном: иначе клиент сочтёт запрос принятым и сотрёт свой журнал.
func TestAck_ErrorsAreNeverTemplated(t *testing.T) {
	nodes := ackNodes(t, ackSpecFor(ackspec.OnErrorDefault))
	r, queue := newShortURLHandler(t, nodes, nil)

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"unknown node", "/api/v1/requestAsync/webhook/nope", http.StatusNotFound},
		{"unknown team", "/api/v1/requestAsync/other/acs_sigur", http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postAck(t, r, tc.url, `{"logs":[{"logId":1}]}`)

			require.Equal(t, tc.want, w.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, false, body["result"], "ошибка обязана оставаться ошибкой")
			assert.NotContains(t, w.Body.String(), "confirmedLogId")
		})
	}
	assert.Equal(t, 0, queue.published)
}
