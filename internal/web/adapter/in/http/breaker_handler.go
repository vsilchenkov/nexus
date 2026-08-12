package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// BreakerHandler — состояние и ручной сброс circuit breaker'а узла (§81.4).
type BreakerHandler struct {
	uc     *usecase.NodeBreakerUsecase
	logger logging.Logger
}

func NewBreakerHandler(uc *usecase.NodeBreakerUsecase, logger logging.Logger) *BreakerHandler {
	return &BreakerHandler{uc: uc, logger: logger}
}

// breakerStateDTO — состояние защиты узла.
//
// available=false означает, что breaker'а в этой инсталляции нет вообще (Redis
// не сконфигурирован) — интерфейс прячет карточку. exists=false — breaker есть,
// но по этому узлу ни разу не срабатывал. threshold=0 — политика неизвестна
// (состояние записано версией до §81.3): показывать счётчик без знаменателя.
type breakerStateDTO struct {
	Available    bool      `json:"available"`
	Exists       bool      `json:"exists"`
	State        string    `json:"state"` // closed | open | half_open
	Failures     int       `json:"failures"`
	Threshold    int       `json:"threshold"`
	OpenedAt     time.Time `json:"opened_at"`
	RetryAfterMs int64     `json:"retry_after_ms"`
}

// breakerResetDTO — итог ручного сброса.
type breakerResetDTO struct {
	WasOpen           bool   `json:"was_open"`
	PreviousState     string `json:"previous_state"`
	PreviousFailures  int    `json:"previous_failures"`
	NodeStatusCleared bool   `json:"node_status_cleared"`
}

// State godoc
// @Summary  Состояние circuit breaker'а узла (§81.4).
// @Description  Открыта ли защита узла, сколько отказов подряд насчитано, когда она открылась и сколько осталось до пробного запроса. Доступно всем ролям: понимать, почему узел молчит, нужно и наблюдателю. available=false — Redis не сконфигурирован, защиты в инсталляции нет.
// @Tags     nodes
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  breakerStateDTO
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/breaker [get]
func (h *BreakerHandler) State(c *gin.Context) {
	st, err := h.uc.State(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.breakerError(c, err, "breaker.state")
		return
	}
	c.JSON(http.StatusOK, breakerStateDTO{
		Available:    st.Available,
		Exists:       st.Exists,
		State:        st.State,
		Failures:     st.Failures,
		Threshold:    st.Threshold,
		OpenedAt:     st.OpenedAt,
		RetryAfterMs: st.RetryAfter.Milliseconds(),
	})
}

// Reset godoc
// @Summary  Снять блокировку circuit breaker'а узла вручную (§81.4.2).
// @Description  Возвращает узлу полный бюджет попыток немедленно, не дожидаясь паузы, и снимает персистентный бейдж «Down» (§52). Идемпотентно: сброс закрытой защиты — не ошибка. Действие пишется в аудит (node.breaker_reset). Требует роль operator+ (§87).
// @Tags     nodes
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  breakerResetDTO
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/breaker/reset [post]
func (h *BreakerHandler) Reset(c *gin.Context) {
	res, err := h.uc.Reset(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.breakerError(c, err, "breaker.reset")
		return
	}
	c.JSON(http.StatusOK, breakerResetDTO{
		WasOpen:           res.WasOpen,
		PreviousState:     res.PreviousState,
		PreviousFailures:  res.PreviousFailures,
		NodeStatusCleared: res.NodeStatusCleared,
	})
}

func (h *BreakerHandler) breakerError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, usecase.ErrBreakerUnavailable):
		// Штатный отказ (Redis не настроен или не отвечает), а не сбой кода —
		// Debug, чтобы не засорять Sentry, но след для разбора «кнопка не
		// работает» остался (§51.9).
		h.logger.Debug("breaker op unavailable", h.logger.Str("op", op), h.logger.Err(err))
		localizedError(c, http.StatusServiceUnavailable, "breaker.unavailable")
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	default:
		h.logger.ErrorWithOp("breaker op failed", err, op)
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
