package usecase

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

type stubAuditRepo struct {
	entries []*domain.AuditEntry
}

func (s *stubAuditRepo) Write(_ context.Context, e *domain.AuditEntry) error {
	s.entries = append(s.entries, e)
	return nil
}

func (s *stubAuditRepo) List(_ context.Context, _ port.AuditFilter) ([]*domain.AuditEntry, error) {
	return s.entries, nil
}

func (s *stubAuditRepo) DeleteOlderThan(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

func newDryRunUC() (*DryRunUsecase, *stubAuditRepo) {
	repo := &stubAuditRepo{}
	audit := NewAuditUsecase(repo, logging.NewNoop())
	return NewDryRunUsecase(audit, logging.NewNoop()), repo
}

// TestDryRun_StaticNoneOK — happy path: static URL + auth_type=none.
// Все шаги отчёта должны быть ok.
func TestDryRun_StaticNoneOK(t *testing.T) {
	t.Parallel()
	uc, audit := newDryRunUC()
	n := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
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
	if !rep.OK {
		t.Fatalf("want OK=true, got false; steps=%+v", rep.Steps)
	}
	stepByName := map[string]DryRunStep{}
	for _, s := range rep.Steps {
		stepByName[s.Name] = s
	}
	for _, name := range []string{"auth.incoming", "auth.outgoing", "url.resolve", "response"} {
		if stepByName[name].Status != "ok" {
			t.Fatalf("step %s status=%s, want ok", name, stepByName[name].Status)
		}
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit entries: got %d, want 1", len(audit.entries))
	}
	if audit.entries[0].Action != domain.ActionNodeDryRun {
		t.Fatalf("audit action: got %q, want %q", audit.entries[0].Action, domain.ActionNodeDryRun)
	}
}

// TestDryRun_FromRequestMissingURL — режим from_request без url_base параметра
// должен дать failed на url.resolve.
func TestDryRun_FromRequestMissingURL(t *testing.T) {
	t.Parallel()
	uc, _ := newDryRunUC()
	n := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeFromRequest,
		URLParamName:     "url_base",
		AuthType:         domain.AuthTypeNone,
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
	rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
		Node:    n,
		Method:  http.MethodPost,
		Query:   url.Values{},
		Headers: http.Header{},
		UseMock: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rep.OK {
		t.Fatalf("want OK=false; steps=%+v", rep.Steps)
	}
	last := rep.Steps[len(rep.Steps)-1]
	if last.Name != "url.resolve" || last.Status != "failed" {
		t.Fatalf("last step want (url.resolve, failed), got (%s, %s)", last.Name, last.Status)
	}
}

// TestDryRun_MaskAuthHeader — auth.outgoing шаг должен показать
// замаскированное значение, а не реальный токен.
func TestDryRun_MaskAuthHeader(t *testing.T) {
	t.Parallel()
	uc, _ := newDryRunUC()
	n := &domain.Node{
		Path:             "demo/path",
		RootMethod:       domain.RootMethodRequest,
		URLMode:          domain.URLModeStatic,
		TargetURL:        "https://example.com/hook",
		AuthType:         domain.AuthTypeToken,
		AuthCredentials:  "supersecrettoken123",
		IncomingAuthType: domain.IncomingAuthTypeNone,
		Status:           domain.NodeStatusEnabled,
	}
	rep, err := uc.Run(context.Background(), SystemActor(), DryRunRequest{
		Node:    n,
		Method:  http.MethodPost,
		UseMock: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for _, s := range rep.Steps {
		if s.Name == "auth.outgoing" {
			if s.Message != "Bearer ***" {
				t.Fatalf("auth.outgoing leaked credentials: %q", s.Message)
			}
			return
		}
	}
	t.Fatal("auth.outgoing step missing")
}
