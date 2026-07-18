package usecase

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"nexus/internal/domain"
)

func incomingBasicNode() *domain.Node {
	return &domain.Node{
		Path:                    "demo/path",
		RootMethod:              domain.RootMethodRequest,
		URLMode:                 domain.URLModeStatic,
		TargetURL:               "https://example.com/hook",
		AuthType:                domain.AuthTypeNone,
		IncomingAuthType:        domain.IncomingAuthTypeBasic,
		IncomingAuthCredentials: "user:pass",
		Status:                  domain.NodeStatusEnabled,
	}
}

func stepByName(rep *DryRunReport, name string) (DryRunStep, bool) {
	for _, s := range rep.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return DryRunStep{}, false
}

// TestDryRun_AutofillIncomingBasic: узел с входящей basic-авторизацией и
// заведёнными кредами, оператор не заполнил заголовок → сервер подставляет креду
// сам, auth.incoming зелёный с autofilled=true, и запрос идёт дальше (§55.6).
func TestDryRun_AutofillIncomingBasic(t *testing.T) {
	t.Parallel()
	uc, _ := newDryRunUC()
	rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
		Node:    incomingBasicNode(),
		Method:  http.MethodPost,
		Query:   url.Values{},
		Headers: http.Header{}, // оператор ничего не ввёл
		Body:    []byte(`{"x":1}`),
		UseMock: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	step, ok := stepByName(rep, "auth.incoming")
	if !ok || step.Status != "ok" {
		t.Fatalf("auth.incoming: got %+v, want ok", step)
	}
	detail, _ := step.Detail.(map[string]any)
	if detail["autofilled"] != true {
		t.Fatalf("auth.incoming detail: want autofilled=true, got %+v", detail)
	}
	if detail["field"] != "Authorization" || detail["source"] != "header" {
		t.Fatalf("auth.incoming detail: want field=Authorization source=header, got %+v", detail)
	}
	// Прогон дошёл до конца (не оборвался на auth.incoming).
	if !rep.OK {
		t.Fatalf("want OK=true; steps=%+v", rep.Steps)
	}
}

// TestDryRun_ManualIncomingAuthWins: если оператор задал заголовок сам, сервер
// не перезатирает его автоподстановкой — неверный ручной ввод остаётся неверным
// (негативный сценарий §55.8 проверяем именно им).
func TestDryRun_ManualIncomingAuthWins(t *testing.T) {
	t.Parallel()
	uc, _ := newDryRunUC()
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:wrong")))
	rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
		Node:    incomingBasicNode(),
		Method:  http.MethodPost,
		Query:   url.Values{},
		Headers: h,
		Body:    []byte(`{"x":1}`),
		UseMock: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	step, ok := stepByName(rep, "auth.incoming")
	if !ok || step.Status != "failed" {
		t.Fatalf("auth.incoming: got %+v, want failed (manual wrong value must not be autofilled over)", step)
	}
	if rep.OK {
		t.Fatal("want OK=false: wrong manual credential must fail")
	}
}

// TestDryRun_AutofillMasksSecretInReport: если узел форвардит своё auth-поле,
// автоподставленная креда не должна утечь открытым текстом в отчёт.
func TestDryRun_AutofillMasksSecretInReport(t *testing.T) {
	t.Parallel()
	uc, _ := newDryRunUC()
	n := incomingBasicNode()
	n.ForwardHeaders = []string{"Authorization"} // узел форвардит входящий заголовок
	rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
		Node:    n,
		Method:  http.MethodPost,
		Query:   url.Values{},
		Headers: http.Header{},
		Body:    []byte(`{"x":1}`),
		UseMock: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// В отчёте headers.forwarded значение Authorization должно быть замаскировано.
	step, ok := stepByName(rep, "headers.forwarded")
	if !ok {
		t.Fatal("headers.forwarded step missing")
	}
	fwd, _ := step.Detail.(map[string]string)
	if got := fwd["Authorization"]; got != "Basic ***" {
		t.Fatalf("headers.forwarded leaked/misformatted Authorization: %q", got)
	}
	// Сам секрет (base64 кред) не должен встречаться нигде в отчёте.
	secret := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	blob, _ := json.Marshal(rep)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("report leaks stored incoming credential: %s", blob)
	}
}
