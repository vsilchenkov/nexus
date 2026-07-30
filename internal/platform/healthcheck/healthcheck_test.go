package healthcheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { gin.SetMode(gin.TestMode) }

// stubChecker — табличный stub для управления результатом Check().
type stubChecker struct {
	name string
	err  error
	slow time.Duration
}

func (s stubChecker) Name() string { return s.name }

func (s stubChecker) Check(ctx context.Context) error {
	if s.slow > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.slow):
		}
	}
	return s.err
}

func TestHandlerLive_AlwaysOK(t *testing.T) {
	t.Parallel()

	r := gin.New()
	New(nil, nil).Register(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestHandlerReady(t *testing.T) {
	t.Parallel()

	boom := errors.New("connection refused")

	tests := []struct {
		name           string
		required       []Checker
		optional       []Checker
		wantStatus     int
		wantReady      bool
		wantDegraded   bool
		wantCheckParts map[string]string
	}{
		{
			name:           "all_up",
			required:       []Checker{stubChecker{name: "pg"}, stubChecker{name: "redis"}},
			optional:       []Checker{stubChecker{name: "clickhouse"}},
			wantStatus:     http.StatusOK,
			wantReady:      true,
			wantDegraded:   false,
			wantCheckParts: map[string]string{"pg": "ok", "redis": "ok", "clickhouse": "ok"},
		},
		{
			name:           "required_down_503",
			required:       []Checker{stubChecker{name: "pg", err: boom}},
			optional:       nil,
			wantStatus:     http.StatusServiceUnavailable,
			wantReady:      false,
			wantDegraded:   false,
			wantCheckParts: map[string]string{"pg": "down: connection refused"},
		},
		{
			name:           "optional_down_degraded",
			required:       []Checker{stubChecker{name: "pg"}},
			optional:       []Checker{stubChecker{name: "clickhouse", err: boom}},
			wantStatus:     http.StatusOK,
			wantReady:      true,
			wantDegraded:   true,
			wantCheckParts: map[string]string{"pg": "ok", "clickhouse": "down: connection refused"},
		},
		{
			name: "required_takes_precedence_over_optional",
			// Required лежит — статус 503, не важно что optional ок.
			required:       []Checker{stubChecker{name: "pg", err: boom}},
			optional:       []Checker{stubChecker{name: "clickhouse"}},
			wantStatus:     http.StatusServiceUnavailable,
			wantReady:      false,
			wantDegraded:   false,
			wantCheckParts: map[string]string{"pg": "down: connection refused", "clickhouse": "ok"},
		},
		{
			name:         "no_checkers_ready_true",
			required:     nil,
			optional:     nil,
			wantStatus:   http.StatusOK,
			wantReady:    true,
			wantDegraded: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := gin.New()
			New(tc.required, tc.optional).Register(r)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))

			require.Equal(t, tc.wantStatus, w.Code, "body=%s", w.Body.String())

			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			assert.Equal(t, tc.wantReady, resp["ready"])

			if tc.wantDegraded {
				assert.Equal(t, true, resp["degraded"])
			} else {
				_, hasDegraded := resp["degraded"]
				assert.False(t, hasDegraded, "не ожидали поле degraded в ответе")
			}

			if tc.wantCheckParts != nil {
				checks, ok := resp["checks"].(map[string]any)
				require.True(t, ok, "checks должны быть map[string]any")
				for k, want := range tc.wantCheckParts {
					assert.Equal(t, want, checks[k], "checks[%q]", k)
				}
			}
		})
	}
}

func TestHandlerReady_TimeoutPropagatesToCheckers(t *testing.T) {
	t.Parallel()

	// Checker, который «висит» дольше, чем handler ему даёт. Разрыв масштабов
	// намеренно большой (5с против Timeout 20мс): проверяемый симптом — «handler
	// дождался чекера вместо дедлайна», и отличать его надо с запасом. С прежними
	// 200мс порог 150мс отделял норму от симптома всего на 50мс, и на
	// перегруженном CI-раннере (4 параллельных job'а) тест падал на 181мс — как
	// флак, а не как регрессия.
	slow := stubChecker{name: "stuck", slow: 5 * time.Second}

	h := &Handler{
		Required: []Checker{slow},
		Timeout:  20 * time.Millisecond,
	}
	r := gin.New()
	h.Register(r)

	w := httptest.NewRecorder()
	start := time.Now()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	elapsed := time.Since(start)

	// Handler должен вернуть 503 за время, близкое к Timeout (≪ slow=5s). Порог
	// 2с ловит регрессию (там было бы ~5с) и переживает джиттер планировщика.
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Less(t, elapsed, 2*time.Second, "ready висит дольше Timeout")
	assert.Contains(t, w.Body.String(), "down:")
	assert.Contains(t, w.Body.String(), "context deadline exceeded")
}

func TestCheckerFunc_NameAndCheck(t *testing.T) {
	t.Parallel()

	want := errors.New("synthetic")
	c := CheckerFunc{
		N: "x",
		F: func(_ context.Context) error { return want },
	}
	assert.Equal(t, "x", c.Name())
	assert.ErrorIs(t, c.Check(context.Background()), want)
}
