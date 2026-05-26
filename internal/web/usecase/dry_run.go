package usecase

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	rcv "bus/internal/receiver/usecase"
)

// DryRunRequest — что приходит в POST /api/nodes/dry-run.
//
// Node — полная (несохранённая) конфигурация узла из формы. Request —
// синтетический запрос, который пользователь хочет «прогнать» через
// шину. UseMock=true (по умолчанию) — реальный HTTP-вызов внешнего
// узла НЕ выполняется; Sender замещается фиктивным 200-ответом.
type DryRunRequest struct {
	Node    *domain.Node
	Method  string
	Query   url.Values
	Headers http.Header
	Body    []byte
	UseMock bool
}

// DryRunStep — один шаг отчёта (§7.5.1 ТЗ).
type DryRunStep struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok | failed | skipped
	Message string `json:"message,omitempty"`
	Detail  any    `json:"detail,omitempty"`
}

// DryRunReport — структурированный пошаговый отчёт о прогоне.
type DryRunReport struct {
	ID    string       `json:"id"`
	Steps []DryRunStep `json:"steps"`
	OK    bool         `json:"ok"`
}

// DryRunUsecase — реализует §7.5.1 (тестовый запрос).
type DryRunUsecase struct {
	audit  *AuditUsecase
	logger logging.Logger
}

func NewDryRunUsecase(audit *AuditUsecase, logger logging.Logger) *DryRunUsecase {
	return &DryRunUsecase{audit: audit, logger: logger}
}

// Run выполняет шаги pipeline'а Receiver на копии конфига узла из формы
// и возвращает структурированный отчёт. Никаких побочных эффектов кроме
// записи в audit.
func (u *DryRunUsecase) Run(ctx context.Context, actor Actor, req DryRunRequest) (*DryRunReport, error) {
	if req.Node == nil {
		return nil, errors.New("dry-run: node is required")
	}
	req.Node.SetDefaults()
	if err := req.Node.Validate(); err != nil {
		return nil, err
	}

	rep := &DryRunReport{ID: uuid.NewString(), OK: true}

	// 1. Incoming auth.
	if err := rcv.CheckIncomingAuth(req.Node, req.Headers, req.Body); err != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "auth.incoming", Status: "failed", Message: err.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:auth.incoming")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "auth.incoming", Status: "ok",
		Message: fmt.Sprintf("type=%s", req.Node.IncomingAuthType),
	})

	// 2. Outgoing auth + cleanup query/headers/body.
	effHeaders := cloneHeader(req.Headers)
	effQuery := cloneValues(req.Query)
	effBody := req.Body
	var authHeader string
	var authErr error
	if req.Node.AuthType.IsDynamic() {
		dyn, derr := rcv.BuildDynamicOutgoingAuth(req.Node, req.Headers, req.Query, req.Body)
		if derr != nil {
			authErr = derr
		} else {
			authHeader = dyn.Header
			effHeaders = dyn.Headers
			effQuery = dyn.Query
			effBody = dyn.Body
		}
	} else {
		authHeader, authErr = rcv.BuildOutgoingAuth(req.Node)
	}
	if authErr != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "auth.outgoing", Status: "failed", Message: authErr.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:auth.outgoing")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name:    "auth.outgoing",
		Status:  "ok",
		Message: maskAuthHeader(authHeader),
		Detail: map[string]any{
			"type":   string(req.Node.AuthType),
			"length": len(authHeader),
		},
	})

	// 3. URL resolve.
	target, cleanQuery, urlErr := rcv.ResolveURL(req.Node, effQuery)
	if urlErr != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "url.resolve", Status: "failed", Message: urlErr.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:url.resolve")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	finalURL := appendQueryToURL(target, cleanQuery)
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "url.resolve", Status: "ok", Message: finalURL,
		Detail: map[string]any{
			"mode":             string(req.Node.URLMode),
			"allowlist_passed": true,
		},
	})

	// 4. Headers forwarded.
	fwd := map[string]string{}
	for _, name := range req.Node.ForwardHeaders {
		if v := effHeaders.Get(name); v != "" {
			fwd[http.CanonicalHeaderKey(name)] = v
		}
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "headers.forwarded", Status: "ok", Detail: fwd,
	})

	// 5. Body sent.
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "body.sent", Status: "ok",
		Detail: map[string]any{
			"size_bytes": len(effBody),
			"preview":    bodyPreview(effBody, 256),
		},
	})

	// 6. Response (mock).
	if !req.UseMock {
		// v1 поддерживает только mock-режим (см. §7.5.1 ТЗ комментарий).
		// Реальный outbound HTTP — TODO Phase 4.
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "response", Status: "skipped",
			Message: "real outbound mode is not supported in v1; use mock",
		})
	} else {
		mockLatency := 50 * time.Millisecond
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "response", Status: "ok",
			Detail: map[string]any{
				"status":      200,
				"duration_ms": int(mockLatency / time.Millisecond),
				"body":        `{"mock":true}`,
				"source":      "mock-server",
			},
		})
	}

	// 7. Would log to ClickHouse.
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "clickhouse.would_log", Status: "ok",
		Detail: map[string]any{
			"table":  req.Node.ClickHouseTable,
			"id":     rep.ID,
			"url":    finalURL,
			"method": req.Method,
		},
	})

	u.writeAudit(ctx, actor, req.Node, rep, "ok")
	return rep, nil
}

func (u *DryRunUsecase) writeAudit(ctx context.Context, actor Actor, n *domain.Node, rep *DryRunReport, outcome string) {
	u.audit.Log(ctx, actor, domain.ActionNodeDryRun, "node", n.ID, map[string]any{
		"path":    n.Path,
		"dry_run": rep.ID,
		"outcome": outcome,
	})
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vv := range v {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func appendQueryToURL(target string, q url.Values) string {
	if len(q) == 0 {
		return target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	existing := parsed.Query()
	for k, vv := range q {
		for _, v := range vv {
			existing.Add(k, v)
		}
	}
	parsed.RawQuery = existing.Encode()
	return parsed.String()
}

// maskAuthHeader заменяет всё, что после "Basic "/"Bearer ", на ***.
// Используется для лога/отчётов — реальное значение не отдаётся клиенту.
func maskAuthHeader(h string) string {
	if h == "" {
		return ""
	}
	for _, prefix := range []string{"Basic ", "Bearer "} {
		if strings.HasPrefix(h, prefix) {
			return prefix + "***"
		}
	}
	return "***"
}

func bodyPreview(b []byte, max int) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
