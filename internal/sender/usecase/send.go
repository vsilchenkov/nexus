// Package usecase — бизнес-логика Sender Service.
package usecase

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"bus/internal/domain"
	"bus/internal/platform/logging"
	"bus/internal/sender/usecase/port"
)

// SendInput — параметры одного outbound-вызова.
type SendInput struct {
	ID         string
	NodePath   string
	RootMethod domain.RootMethod

	TargetURL string
	Method    string
	Headers   map[string]string
	Body      []byte

	TimeoutMs      int32
	RetryCount     int32
	RetryBackoffMs int32

	ClickHouseTable string
	LogRequestBody  bool
	LogResponseBody bool
	LogHeaders      bool
	ClientIP        string
}

// SendOutput — результат, который Sender отдаёт обратно Receiver'у.
type SendOutput struct {
	StatusCode int32
	Headers    map[string]string
	Body       []byte
	Error      string
	Attempts   int32
	DurationMs int32
}

// CircuitBreaker — interface circuit breaker'а (§9.5 ТЗ).
// Объявлен здесь, чтобы usecase не зависел от Redis-реализации.
// Реализация — internal/platform/circuitbreaker.
type CircuitBreaker interface {
	Allow(ctx context.Context, key string) (bool, error)
	RecordSuccess(ctx context.Context, key string) error
	RecordFailure(ctx context.Context, key string) error
}

// noopBreaker используется, если CB отключён (cfg-зависимость не настроена).
type noopBreaker struct{}

func (noopBreaker) Allow(context.Context, string) (bool, error)  { return true, nil }
func (noopBreaker) RecordSuccess(context.Context, string) error  { return nil }
func (noopBreaker) RecordFailure(context.Context, string) error  { return nil }

// SendUsecase — оркестрация: HTTP-вызов с retry + асинхронная запись лога в ClickHouse.
type SendUsecase struct {
	httpc  port.HTTPCaller
	logw   port.LogWriter
	cb     CircuitBreaker
	logger logging.Logger
	host   string
}

func NewSendUsecase(httpc port.HTTPCaller, logw port.LogWriter, cb CircuitBreaker, logger logging.Logger) *SendUsecase {
	if cb == nil {
		cb = noopBreaker{}
	}
	host, _ := os.Hostname()
	return &SendUsecase{httpc: httpc, logw: logw, cb: cb, logger: logger, host: host}
}

type attempt struct {
	N               int32     `json:"n"`
	StartedAt       time.Time `json:"started_at"`
	DurationMs      int32     `json:"duration_ms"`
	Status          int32     `json:"status"`
	Reason          string    `json:"reason"`
	BackoffBeforeMs int32     `json:"backoff_before_ms"`
}

func (u *SendUsecase) Send(ctx context.Context, in SendInput) SendOutput {
	t0 := time.Now()
	rec := &domain.LogRecord{
		ID:              in.ID,
		Type:            in.RootMethod,
		URL:             in.TargetURL,
		Method:          in.Method,
		Parameters:      extractQuery(in.TargetURL),
		ChecksumRequest: md5hex(in.Body),
		DateCreate:      t0,
		DateRequest:     t0,
		Host:            u.host,
		IP:              in.ClientIP,
	}
	if in.LogRequestBody {
		rec.Request = string(in.Body)
	}

	req := &port.HTTPRequest{
		Method:    in.Method,
		URL:       in.TargetURL,
		Headers:   in.Headers,
		Body:      in.Body,
		TimeoutMs: in.TimeoutMs,
	}

	// Circuit breaker per node (§9.5). Если breaker open — сразу
	// 503 без попытки + лог. Это снижает нагрузку на проблемный
	// внешний узел и ускоряет fail-fast в DLQ для async.
	if allowed, _ := u.cb.Allow(ctx, in.NodePath); !allowed {
		rec.DateResponse = time.Now()
		rec.Duration = int32(time.Since(t0).Milliseconds())
		rec.Status = 0
		rec.Done = false
		rec.Reason = "circuit_breaker_open"
		rec.Attempts = 0
		u.logw.Write(ctx, in.ClickHouseTable, rec)
		return SendOutput{
			StatusCode: 503,
			Error:      "circuit breaker open",
			DurationMs: rec.Duration,
		}
	}

	var (
		resp      *port.HTTPResponse
		lastErr   error
		attempts  []attempt
		backoffMs int32
	)
	maxAttempts := in.RetryCount + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for n := int32(1); n <= maxAttempts; n++ {
		if backoffMs > 0 {
			time.Sleep(time.Duration(backoffMs) * time.Millisecond)
		}
		started := time.Now()
		resp, lastErr = u.httpc.Do(ctx, req)
		dur := int32(time.Since(started).Milliseconds())

		a := attempt{N: n, StartedAt: started, DurationMs: dur, BackoffBeforeMs: backoffMs}
		if lastErr != nil {
			a.Status = 0
			a.Reason = lastErr.Error()
		} else {
			a.Status = resp.StatusCode
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				a.Reason = "OK"
			} else {
				a.Reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
		attempts = append(attempts, a)

		if lastErr == nil && resp != nil && resp.StatusCode < 500 {
			break // успех или 4xx — retry не помогает
		}
		// exponential backoff with full jitter
		base := in.RetryBackoffMs
		if base <= 0 {
			base = 100
		}
		backoffMs = int32(rand.Intn(int(base * (1 << min(int(n-1), 5)))))
	}

	out := SendOutput{
		Attempts:   int32(len(attempts)),
		DurationMs: int32(time.Since(t0).Milliseconds()),
	}
	rec.DateResponse = time.Now()
	rec.Duration = out.DurationMs
	rec.Attempts = out.Attempts

	switch {
	case lastErr != nil:
		out.Error = lastErr.Error()
		rec.Status = 0
		rec.Done = false
		rec.Reason = lastErr.Error()
	case resp != nil:
		out.StatusCode = resp.StatusCode
		out.Headers = resp.Headers
		out.Body = resp.Body
		rec.Status = resp.StatusCode
		rec.Done = resp.StatusCode >= 200 && resp.StatusCode < 300
		rec.ChecksumResponse = md5hex(resp.Body)
		if in.LogResponseBody {
			rec.Response = string(resp.Body)
		}
		if !rec.Done {
			rec.Reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			rec.Reason = "OK"
		}
	}

	// Обновляем circuit breaker по итогу.
	if rec.Done {
		_ = u.cb.RecordSuccess(ctx, in.NodePath)
	} else {
		_ = u.cb.RecordFailure(ctx, in.NodePath)
	}

	if len(attempts) > 1 || !rec.Done {
		if data, err := json.Marshal(attempts); err == nil {
			rec.AttemptsDetails = string(data)
		}
	}

	u.logw.Write(ctx, in.ClickHouseTable, rec)
	return out
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// extractQuery возвращает query-string из URL без ведущего "?".
// Чувствительные значения (токены) предполагается, что уже исключены
// Receiver'ом перед передачей в Sender (§3.5).
func extractQuery(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.RawQuery
}

// strings импортирован неявно для будущих расширений (mask логики).
var _ = strings.TrimSpace
