// Package healthcheck — handlers для /health (liveness) и /ready (readiness).
//
// Соответствует §9.6 ТЗ:
//   - /health — 200, пока процесс жив;
//   - /ready  — 200, если все обязательные зависимости отвечают;
//     200 + degraded:true, если опциональная зависимость лежит;
//     503, если упала обязательная зависимость.
package healthcheck

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Checker — описывает одну проверяемую зависимость.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckerFunc — адаптер для функций.
type CheckerFunc struct {
	N string
	F func(ctx context.Context) error
}

func (c CheckerFunc) Name() string                    { return c.N }
func (c CheckerFunc) Check(ctx context.Context) error { return c.F(ctx) }

// Handler собирает Gin-handler'ы /health и /ready.
//
//	required — без этих зависимостей сервис не может работать (503).
//	optional — деградация: ready=true, degraded=true.
type Handler struct {
	Required []Checker
	Optional []Checker
	Timeout  time.Duration
}

// New — handler с timeout 1с по умолчанию на каждый чек.
func New(required, optional []Checker) *Handler {
	return &Handler{
		Required: required,
		Optional: optional,
		Timeout:  time.Second,
	}
}

// Live — handler для /health. Всегда 200, пока процесс жив.
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready — handler для /ready.
func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.Timeout)
	defer cancel()

	checks := make(map[string]string, len(h.Required)+len(h.Optional))
	allRequiredUp := true
	anyOptionalDown := false

	for _, ch := range h.Required {
		if err := ch.Check(ctx); err != nil {
			checks[ch.Name()] = "down: " + err.Error()
			allRequiredUp = false
		} else {
			checks[ch.Name()] = "ok"
		}
	}
	for _, ch := range h.Optional {
		if err := ch.Check(ctx); err != nil {
			checks[ch.Name()] = "down: " + err.Error()
			anyOptionalDown = true
		} else {
			checks[ch.Name()] = "ok"
		}
	}

	status := http.StatusOK
	resp := gin.H{"ready": true, "checks": checks}

	if !allRequiredUp {
		status = http.StatusServiceUnavailable
		resp["ready"] = false
	} else if anyOptionalDown {
		resp["degraded"] = true
	}
	c.JSON(status, resp)
}

// Register регистрирует /health и /ready в Gin-роутере.
func (h *Handler) Register(r *gin.Engine) {
	r.GET("/health", h.Live)
	r.GET("/ready", h.Ready)
}
