package usecase

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
	senderv1 "nexus/proto/sender/v1"
)

// stubSender — port.SenderClient: фиксирует, ЧТО ушло в Sender, и отдаёт
// заданный ответ. Через него проверяются гейты §55 (dry_run/logging_enabled) и
// изолированный node_path.
type stubSender struct {
	got  *senderv1.SendRequest
	resp *senderv1.SendResponse
	err  error
}

func (s *stubSender) Send(_ context.Context, req *senderv1.SendRequest) (*senderv1.SendResponse, error) {
	s.got = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func newDryRunUCWithSender(sender port.SenderClient, selfHosts []string) (*DryRunUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	audit := NewAuditUsecase(repo, logging.NewNoop())
	return NewDryRunUsecase(audit, sender, selfHosts, logging.NewNoop()), repo
}

// stepOf — шаг отчёта по имени.
func stepOf(t *testing.T, rep *DryRunReport, name string) DryRunStep {
	t.Helper()
	for _, s := range rep.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("шаг %q отсутствует в отчёте", name)
	return DryRunStep{}
}

func dryRunNode() *domain.Node {
	return &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
		TimeoutMs:        1500,
		RetryCount:       2,
		ClickHouseTable:  "nexus.demo",
		LoggingEnabled:   true, // узел логирует — dry-run всё равно не должен писать
	}
}

func dryRunReq(n *domain.Node, useMock bool) DryRunRequest {
	return DryRunRequest{
		Node:    n,
		Method:  http.MethodPost,
		Query:   url.Values{},
		Headers: http.Header{},
		Body:    []byte(`{"test":true}`),
		UseMock: useMock,
	}
}

// §55.4: главная гарантия — что именно Web просит у Sender'а. Забытый гейт
// здесь = боевой инцидент (запись в production-таблицу, узел в Down, открытый
// breaker), поэтому проверяем поля запроса поимённо.
func TestDryRun_RealCall_SendsGatesToSender(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{
		StatusCode: 200, Body: []byte(`{"real":true}`), DurationMs: 42, Attempts: 1,
		Headers: map[string]string{"Content-Type": "application/json"},
	}}
	uc, audit := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err)
	require.True(t, rep.OK)

	require.NotNil(t, sender.got, "реальный режим обязан дойти до Sender")
	assert.True(t, sender.got.GetDryRun(), "флаг dry_run: гасит метрики/статус/breaker в Sender")
	assert.False(t, sender.got.GetLoggingEnabled(),
		"logging_enabled=false: второй гейт — тест не пишет в production-таблицу даже при logging_enabled узла")
	assert.True(t, strings.HasPrefix(sender.got.GetNodePath(), dryRunPathPrefix),
		"node_path изолирован (%q) — страховка на случай забытого гейта", sender.got.GetNodePath())
	assert.NotEqual(t, "demo/path", sender.got.GetNodePath(), "боевой путь узла не используется")

	// Конфиг узла доезжает как есть — вызов идёт боевым путём.
	assert.Equal(t, "https://example.com/hook", sender.got.GetTargetUrl())
	assert.Equal(t, http.MethodPost, sender.got.GetMethod())
	assert.Equal(t, int32(1500), sender.got.GetTimeoutMs(), "таймаут узла = таймаут теста")
	assert.Equal(t, int32(2), sender.got.GetRetryCount(), "ретраи воспроизводятся как в бою")
	assert.Equal(t, []byte(`{"test":true}`), sender.got.GetBody())

	// Отчёт показывает НАСТОЯЩИЙ ответ, а не синтетику.
	step := stepOf(t, rep, "response")
	assert.Equal(t, "ok", step.Status)
	d, ok := step.Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int32(200), d["status"])
	assert.Equal(t, int32(42), d["duration_ms"])
	assert.Equal(t, int32(1), d["attempts"])
	assert.Equal(t, "sender", d["source"], "источник — Sender, не mock")

	require.Len(t, audit.entries, 1)
	assert.Equal(t, domain.ActionNodeDryRun, audit.entries[0].Action)
}

// Ради этого §55 и делался: боевой кейс «узел висит 30с» должен быть виден.
func TestDryRun_RealCall_TargetTimeout(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{
		StatusCode: 0, // ответа нет
		Error:      `Post "http://task.example/hs": context deadline exceeded`,
		DurationMs: 30002, Attempts: 1,
	}}
	uc, audit := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err, "узел не ответил — это результат теста, а не сбой операции")
	assert.False(t, rep.OK)

	step := stepOf(t, rep, "response")
	assert.Equal(t, "failed", step.Status)
	assert.Contains(t, step.Message, "context deadline exceeded", "оператор видит настоящую причину")
	d, ok := step.Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int32(30002), d["duration_ms"])

	require.Len(t, audit.entries, 1)
	assert.Equal(t, "failed:response", audit.entries[0].Details["outcome"],
		"в журнале видно, что реальный вызов не удался")
}

// HTTP-ошибка узла (4xx/5xx) — ответ получен: шаг успешен, код виден в отчёте.
func TestDryRun_RealCall_HTTPErrorIsAnswer(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 401, Body: []byte("unauthorized"), Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err)

	step := stepOf(t, rep, "response")
	assert.Equal(t, "ok", step.Status, "401 — это ответ узла, оператор решает сам")
	d, ok := step.Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int32(401), d["status"])
}

// Сбой самого инструмента (Sender недоступен) отличается от «узел не ответил».
func TestDryRun_RealCall_SenderUnavailable(t *testing.T) {
	t.Parallel()

	sender := &stubSender{err: errors.New("connection refused")}
	uc, _ := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err)
	assert.False(t, rep.OK)

	step := stepOf(t, rep, "response")
	assert.Equal(t, "failed", step.Status)
	assert.Contains(t, step.Message, "sender is unavailable")
}

// Реальный режим без сконфигурированного Sender'а — понятный skipped, а не
// падение: Web не обязан знать про Sender ради mock-сценария.
func TestDryRun_RealCall_NoSenderConfigured(t *testing.T) {
	t.Parallel()

	uc, _ := newDryRunUCWithSender(nil, nil)
	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err)

	step := stepOf(t, rep, "response")
	assert.Equal(t, "skipped", step.Status)
	assert.Contains(t, step.Message, "web.sender_grpc.addr")
	assert.True(t, rep.OK, "не сконфигурировано — не ошибка конфига узла")
}

// §32.2: реальный вызов на собственный ingress — петля. В mock-режиме проверки
// нет (запрос никуда не уходит), в реальном обязана быть.
func TestDryRun_RealCall_SelfReferenceRejected(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200}}
	uc, _ := newDryRunUCWithSender(sender, []string{"nexus.example.ru"})

	n := dryRunNode()
	n.TargetURL = "https://nexus.example.ru/api/v1/request/team/node"

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(n, false))
	require.NoError(t, err)
	assert.False(t, rep.OK)

	step := stepOf(t, rep, "response")
	assert.Equal(t, "failed", step.Status)
	assert.Nil(t, sender.got, "до Sender'а запрос дойти не должен — петля обрывается раньше")
}

// mock (по умолчанию) не ходит в Sender вовсе — зависимость Web→Sender
// опциональна, и побочных эффектов во внешней системе нет.
func TestDryRun_Mock_DoesNotCallSender(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), true))
	require.NoError(t, err)
	require.True(t, rep.OK)

	assert.Nil(t, sender.got, "mock не должен слать реальный запрос")
	d, ok := stepOf(t, rep, "response").Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "mock-server", d["source"])
	// §7.5.1: задержка mock — случайная 50–100 мс.
	assert.GreaterOrEqual(t, d["duration_ms"], 50)
	assert.LessOrEqual(t, d["duration_ms"], 100)
}

// §55.9 / §40: узел с фиксированным outgoing_method переопределяет метод формы.
// До этого dry-run слал метод формы как есть — тест узла с outgoing_method=PUT
// бил PUT'ом в проде и POST'ом в тесте.
func TestDryRun_RealCall_OutgoingMethodOverridesForm(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200, Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.IncomingMethod = domain.HTTPMethodAny // принимаем что угодно
	n.OutgoingMethod = domain.HTTPMethodPUT // но наружу всегда PUT

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(n, false)) // форма шлёт POST
	require.NoError(t, err)
	require.True(t, rep.OK)

	assert.Equal(t, http.MethodPut, sender.got.GetMethod(),
		"в target должен уйти метод узла, а не метод формы")

	d, ok := stepOf(t, rep, "method.incoming").Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, http.MethodPost, d["incoming"])
	assert.Equal(t, http.MethodPut, d["outgoing"], "отчёт показывает подмену метода")
}

// §40: outgoing_method=ANY зеркалит входящий метод.
func TestDryRun_RealCall_OutgoingMethodAnyMirrors(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200, Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.IncomingMethod = domain.HTTPMethodAny
	n.OutgoingMethod = domain.HTTPMethodAny

	req := dryRunReq(n, false)
	req.Method = http.MethodDelete
	rep, err := uc.Run(context.Background(), SystemActor(), req)
	require.NoError(t, err)
	require.True(t, rep.OK)

	assert.Equal(t, http.MethodDelete, sender.got.GetMethod())
}

// §40: узел не принимает такой входящий метод — прод ответил бы отказом, тест
// обязан показать это, а не «успешно» дойти до target.
func TestDryRun_IncomingMethodRejected(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200}}
	uc, audit := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.IncomingMethod = domain.HTTPMethodGET // узел принимает только GET

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(n, false)) // форма шлёт POST
	require.NoError(t, err)
	assert.False(t, rep.OK)

	step := stepOf(t, rep, "method.incoming")
	assert.Equal(t, "failed", step.Status)
	assert.Nil(t, sender.got, "до target запрос дойти не должен")
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "failed:method.incoming", audit.entries[0].Details["outcome"])
}

// §39 / §55.9: хвост пути приклеивается к target — тест passthrough-узла обязан
// бить в реальный URL, а не в базовый адрес.
func TestDryRun_RealCall_PathTailAppended(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200, Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.PathPassthrough = true

	req := dryRunReq(n, false)
	req.PathTail = "orders/42"
	rep, err := uc.Run(context.Background(), SystemActor(), req)
	require.NoError(t, err)
	require.True(t, rep.OK)

	assert.Equal(t, "https://example.com/hook/orders/42", sender.got.GetTargetUrl())
	d, ok := stepOf(t, rep, "url.resolve").Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "orders/42", d["path_tail"])
}

// Тот же код, что в бою: JoinPath режет «../» — хвостом нельзя выйти за пределы
// target_url (иначе тест стал бы SSRF-обходом allowlist §23).
func TestDryRun_RealCall_PathTailCannotEscapeTarget(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200, Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.PathPassthrough = true
	n.TargetURL = "https://example.com/base/hook"

	req := dryRunReq(n, false)
	req.PathTail = "../../../etc/passwd"
	rep, err := uc.Run(context.Background(), SystemActor(), req)
	require.NoError(t, err)
	require.True(t, rep.OK)

	assert.Equal(t, "https://example.com/etc/passwd", sender.got.GetTargetUrl(),
		"хост неизменен — за пределы схемы/хоста target выйти нельзя")
	assert.True(t, strings.HasPrefix(sender.got.GetTargetUrl(), "https://example.com/"))
}

// Узел без passthrough хвост игнорирует (как в бою) и сообщает об этом — иначе
// оператор решит, что тест молча «съел» его ввод.
func TestDryRun_PathTailIgnoredWithoutPassthrough(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{StatusCode: 200, Attempts: 1}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	n := dryRunNode()
	n.PathPassthrough = false

	req := dryRunReq(n, false)
	req.PathTail = "orders/42"
	rep, err := uc.Run(context.Background(), SystemActor(), req)
	require.NoError(t, err)

	assert.Equal(t, "https://example.com/hook", sender.got.GetTargetUrl())
	d, ok := stepOf(t, rep, "url.resolve").Detail.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, d["path_tail_ignored"], "отчёт объясняет, почему хвост не применён")
}

// Эхо-заголовки внешнего узла не должны светить креды в отчёте.
func TestDryRun_RealCall_MasksAuthInResponseHeaders(t *testing.T) {
	t.Parallel()

	sender := &stubSender{resp: &senderv1.SendResponse{
		StatusCode: 200,
		Headers:    map[string]string{"Authorization": "Bearer super-secret-token", "X-Ok": "1"},
	}}
	uc, _ := newDryRunUCWithSender(sender, nil)

	rep, err := uc.Run(context.Background(), SystemActor(), dryRunReq(dryRunNode(), false))
	require.NoError(t, err)

	d, ok := stepOf(t, rep, "response").Detail.(map[string]any)
	require.True(t, ok)
	h, ok := d["headers"].(map[string]string)
	require.True(t, ok)
	assert.NotContains(t, h["Authorization"], "super-secret-token")
	assert.Equal(t, "1", h["X-Ok"], "обычные заголовки не трогаем")
}
