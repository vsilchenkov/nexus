package usecase

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/logging"
)

// sigurAckNode — узел в боевой форме §83: async, шаблон эхо-подтверждения.
func sigurAckNode(onError ackspec.OnError) *domain.Node {
	return &domain.Node{
		ID:             "node-1",
		Path:           "acs_sigur",
		RootMethod:     domain.RootMethodRequestAsync,
		IncomingMethod: domain.HTTPMethodPOST,
		OutgoingMethod: domain.HTTPMethodPOST,
		URLMode:        domain.URLModeStatic,
		TargetURL:      "https://receiver.example/api",
		Status:         domain.NodeStatusEnabled,
		// Явные «нет авторизации»: RouteAsync резолвит узел как есть, мимо
		// SetDefaults (тот работает на записи узла, а не на чтении).
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		AsyncAck: &ackspec.Spec{
			Version:     ackspec.Version,
			ContentType: ackspec.ContentTypeJSON,
			Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
			OnError:     onError,
		},
	}
}

func ackInput(body string) RouteInput {
	return RouteInput{
		NodePath: "acs_sigur",
		Method:   http.MethodPost,
		Header:   http.Header{"Content-Type": {"application/json"}},
		Query:    url.Values{},
		Body:     []byte(body),
		ClientIP: "10.0.0.1",
	}
}

func ackUsecase(t *testing.T, node *domain.Node) (*RouteAsyncUsecase, *stubProducer) {
	t.Helper()
	producer := &stubProducer{}
	return NewRouteAsyncUsecase(stubNodeReader{node: node}, producer, "nexus.async", 5,
		logging.NewNoop()), producer
}

// TestRouteAsync_Ack_Sigur — боевой сценарий целиком: тело СКУД → ответ с
// максимальным logId, сообщение при этом опубликовано.
func TestRouteAsync_Ack_Sigur(t *testing.T) {
	uc, producer := ackUsecase(t, sigurAckNode(ackspec.OnErrorDefault))

	res, err := uc.RouteAsync(context.Background(),
		ackInput(`{"logs":[{"logId":79154,"empId":"000004627"},{"logId":79000}]}`))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Ack == nil {
		t.Fatal("ack response missing")
	}
	if got := string(res.Ack.Body); got != `{"confirmedLogId": 79154}` {
		t.Errorf("body = %s", got)
	}
	if res.Ack.Status != http.StatusOK {
		t.Errorf("status = %d", res.Ack.Status)
	}
	if res.Ack.ContentType != "application/json; charset=utf-8" {
		t.Errorf("content type = %q", res.Ack.ContentType)
	}
	if res.AckDegraded != "" {
		t.Errorf("unexpected degradation %q", res.AckDegraded)
	}
	if producer.calls != 1 {
		t.Errorf("message must be published, calls=%d", producer.calls)
	}
}

// TestRouteAsync_Ack_NoSpec — узел без шаблона отвечает как раньше. Это
// доказательство back-compat: ветка §83 не должна трогать существующие узлы.
func TestRouteAsync_Ack_NoSpec(t *testing.T) {
	node := sigurAckNode(ackspec.OnErrorDefault)
	node.AsyncAck = nil
	uc, producer := ackUsecase(t, node)

	res, err := uc.RouteAsync(context.Background(), ackInput(`{"logs":[{"logId":1}]}`))

	if err != nil {
		t.Fatal(err)
	}
	if res.Ack != nil {
		t.Fatalf("node without spec must keep the old response, got %+v", res.Ack)
	}
	if res.AckDegraded != "" {
		t.Errorf("no spec — no degradation, got %q", res.AckDegraded)
	}
	if producer.calls != 1 {
		t.Errorf("calls=%d", producer.calls)
	}
}

// TestRouteAsync_Ack_DegradeKeepsMessage — политика default: шаблон не смог,
// но запрос ПРИНЯТ. Отказывать из-за шаблона на этой ветке нельзя.
func TestRouteAsync_Ack_DegradeKeepsMessage(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		header     http.Header
		wantReason string
	}{
		{
			name:       "body is not json",
			body:       "--boundary\r\n",
			header:     http.Header{"Content-Type": {"multipart/form-data; boundary=b"}},
			wantReason: string(ackspec.ReasonBodyNotJSON),
		},
		{
			name:       "path not found",
			body:       `{"other":1}`,
			header:     http.Header{"Content-Type": {"application/json"}},
			wantReason: string(ackspec.ReasonPathNotFound),
		},
		{
			name:       "value type mismatch",
			body:       `{"logs":[{"logId":"a"},{"logId":"b"}]}`,
			header:     http.Header{"Content-Type": {"application/json"}},
			wantReason: string(ackspec.ReasonTypeMismatch),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc, producer := ackUsecase(t, sigurAckNode(ackspec.OnErrorDefault))
			in := ackInput(tc.body)
			in.Header = tc.header

			res, err := uc.RouteAsync(context.Background(), in)

			if err != nil {
				t.Fatalf("default policy must not fail the request: %v", err)
			}
			if res.Ack != nil {
				t.Errorf("degraded render must fall back to the old response, got %+v", res.Ack)
			}
			if res.AckDegraded != tc.wantReason {
				t.Errorf("reason = %q, want %q", res.AckDegraded, tc.wantReason)
			}
			if producer.calls != 1 {
				t.Errorf("message must still be published, calls=%d", producer.calls)
			}
		})
	}
}

// TestRouteAsync_Ack_ErrorPolicyDoesNotPublish — ключевой инвариант §83:
// при on_error=error сообщение НЕ уходит в Kafka. Иначе клиент получил бы 400
// на принятый запрос и повторил пакет — то есть шина сама порождала бы дубли.
func TestRouteAsync_Ack_ErrorPolicyDoesNotPublish(t *testing.T) {
	uc, producer := ackUsecase(t, sigurAckNode(ackspec.OnErrorError))

	res, err := uc.RouteAsync(context.Background(), ackInput(`{"other":1}`))

	if err == nil {
		t.Fatal("want ErrAckRenderFailed")
	}
	if res != nil {
		t.Errorf("result must be nil on failure, got %+v", res)
	}
	if producer.calls != 0 {
		t.Fatalf("message must NOT be published when the request is rejected, calls=%d", producer.calls)
	}
}

// TestRouteAsync_Ack_PausedNode — §3.6: узел на паузе отвечает по шаблону, а
// не {queued:true}; статус остаётся 202, а признак паузы доступен шаблону.
func TestRouteAsync_Ack_PausedNode(t *testing.T) {
	node := sigurAckNode(ackspec.OnErrorDefault)
	node.Status = domain.NodeStatusPaused
	node.AsyncAck.Body = `{"confirmedLogId": "${ body.logs[*].logId | max }", "queued": "${ nexus.queued }"}`
	uc, _ := ackUsecase(t, node)

	res, err := uc.RouteAsync(context.Background(), ackInput(`{"logs":[{"logId":5}]}`))

	if err != nil {
		t.Fatal(err)
	}
	if res.Ack == nil {
		t.Fatal("paused node must answer by template too")
	}
	if res.Ack.Status != http.StatusAccepted {
		t.Errorf("status = %d, want 202 (§3.6 semantics kept)", res.Ack.Status)
	}
	if got := string(res.Ack.Body); got != `{"confirmedLogId": 5, "queued": true}` {
		t.Errorf("body = %s", got)
	}
}

// TestRouteAsync_Ack_ExplicitStatus — заданный в спеке код побеждает, но только
// в пределах 2xx (валидация спеки).
func TestRouteAsync_Ack_ExplicitStatus(t *testing.T) {
	node := sigurAckNode(ackspec.OnErrorDefault)
	node.Status = domain.NodeStatusPaused
	node.AsyncAck.Status = http.StatusOK
	uc, _ := ackUsecase(t, node)

	res, err := uc.RouteAsync(context.Background(), ackInput(`{"logs":[{"logId":5}]}`))

	if err != nil {
		t.Fatal(err)
	}
	if res.Ack.Status != http.StatusOK {
		t.Errorf("explicit status must win over queued default, got %d", res.Ack.Status)
	}
}

// TestRouteAsync_Ack_NoCredentialEcho — шаблон получает тело и query ПОСЛЕ
// вырезания кред: ни исходящая динамическая авторизация (тело), ни входящая
// (query) не должны возвращаться клиенту эхом.
func TestRouteAsync_Ack_NoCredentialEcho(t *testing.T) {
	t.Run("outgoing token from body", func(t *testing.T) {
		node := sigurAckNode(ackspec.OnErrorDefault)
		node.AuthType = domain.AuthTypeTokenFromRequest
		node.AuthDynamicSource = domain.AuthDynSourceBody
		node.AuthDynamicField = "token"
		node.AsyncAck.Body = `{"echo": "${ body.token | default("absent") }"}`
		uc, _ := ackUsecase(t, node)

		res, err := uc.RouteAsync(context.Background(), ackInput(`{"token":"sekret","logs":[]}`))

		if err != nil {
			t.Fatal(err)
		}
		if got := string(res.Ack.Body); got != `{"echo": "absent"}` {
			t.Fatalf("credential leaked into the ack response: %s", got)
		}
	})

	t.Run("incoming token from query", func(t *testing.T) {
		node := sigurAckNode(ackspec.OnErrorDefault)
		node.IncomingAuthType = domain.IncomingAuthTypeToken
		node.IncomingAuthCredentials = "sekret"
		node.IncomingAuthDynamicSource = domain.IncomingAuthSourceQuery
		node.IncomingAuthDynamicField = "access_token"
		node.AsyncAck.Body = `{"echo": "${ query.access_token | default("absent") }"}`
		uc, _ := ackUsecase(t, node)

		in := ackInput(`{"logs":[]}`)
		in.Query = url.Values{"access_token": {"sekret"}}

		res, err := uc.RouteAsync(context.Background(), in)

		if err != nil {
			t.Fatal(err)
		}
		if got := string(res.Ack.Body); got != `{"echo": "absent"}` {
			t.Fatalf("incoming credential leaked into the ack response: %s", got)
		}
	})
}

// TestRouteAsync_Ack_NexusFields — служебные поля доступны шаблону: без них
// саппорт не свяжет ответ клиента с записью журнала.
func TestRouteAsync_Ack_NexusFields(t *testing.T) {
	node := sigurAckNode(ackspec.OnErrorDefault)
	node.AsyncAck.Body = `{"id": "${ nexus.id }", "node": "${ nexus.node }"}`
	uc, _ := ackUsecase(t, node)

	res, err := uc.RouteAsync(context.Background(), ackInput(`{}`))

	if err != nil {
		t.Fatal(err)
	}
	want := `{"id": "` + res.ID + `", "node": "acs_sigur"}`
	if got := string(res.Ack.Body); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}
