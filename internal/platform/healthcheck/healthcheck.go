// Package healthcheck — handlers для /health (liveness) и /ready (readiness).
//
// Соответствует §9.6 ТЗ:
//   - /health — 200, пока процесс жив;
//   - /ready  — 200, если все обязательные зависимости отвечают;
//     200 + degraded:true, если опциональная зависимость лежит;
//     503, если упала обязательная зависимость;
//     503 + draining:true, если сервис получил SIGTERM и доигрывает запросы (§93.5).
package healthcheck

import (
	"context"
	"net/http"
	"sync/atomic"
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

	// draining — сервис получил сигнал остановки и выводится из ротации (§93.5).
	// Взводится один раз и не сбрасывается: обратной дороги из остановки нет.
	draining atomic.Bool
}

// New — handler с timeout 1с по умолчанию на каждый чек.
func New(required, optional []Checker) *Handler {
	return &Handler{
		Required: required,
		Optional: optional,
		Timeout:  time.Second,
	}
}

// StartDraining переводит /ready в 503 (§93.5): «меня можно выводить из
// ротации, новые запросы сюда не нужны». Вызывается из Stop сервиса ПЕРЕД
// остановкой HTTP-сервера, чтобы балансировщик успел перестать слать трафик,
// пока текущие запросы ещё доигрывают.
//
// Идемпотентен. /health при этом продолжает отвечать 200 сознательно: это
// liveness, по нему docker решает, не убить ли контейнер, — а контейнер в
// момент штатной остановки убивать не надо.
func (h *Handler) StartDraining() { h.draining.Store(true) }

// Draining — идёт ли сейчас дренаж. Нужен тестам и диагностике.
func (h *Handler) Draining() bool { return h.draining.Load() }

// Live — handler для /health. Всегда 200, пока процесс жив.
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready — handler для /ready.
func (h *Handler) Ready(c *gin.Context) {
	// Дренаж отвечает раньше проверок зависимостей и без них: опрашивать
	// PostgreSQL с Redis на остановке незачем, а ответ нужен немедленный —
	// балансировщик в этот момент ждёт именно его.
	if h.draining.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"ready":    false,
			"draining": true,
		})
		return
	}

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

// Drain — общая процедура вывода реплики из ротации перед остановкой (§93.5):
// пометить сервис неготовым и выдержать паузу, за которую балансировщик успеет
// это заметить и перестать слать новые запросы.
//
// Вынесена в пакет, а не написана в каждом сервисе: пауза должна начинаться
// строго ПОСЛЕ StartDraining, и три копии этого порядка рано или поздно
// разошлись бы.
//
// nil-handler и неположительная пауза — no-op: одиночная установка, где
// выводить из ротации некуда, не должна платить за это лишними секундами
// остановки.
func Drain(ctx context.Context, h *Handler, pause time.Duration) {
	if h == nil {
		return
	}
	h.StartDraining()
	if pause <= 0 {
		return
	}
	t := time.NewTimer(pause)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Register регистрирует /health и /ready в Gin-роутере.
func (h *Handler) Register(r *gin.Engine) {
	r.GET("/health", h.Live)
	r.GET("/ready", h.Ready)
}
