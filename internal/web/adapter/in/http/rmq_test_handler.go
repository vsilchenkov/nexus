package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/ratelimit"
	"nexus/internal/web/usecase"
)

// RMQTestHandler — POST /api/nodes/test-rmq (§27.8). Диагностический
// AMQP-handshake без сохранения узла. Rate-limit на пользователя, чтобы
// зажатая кнопка «Проверить» не устроила DoS на RabbitMQ.
type RMQTestHandler struct {
	tester      *usecase.RMQTester
	limiter     *ratelimit.Limiter
	limitPerMin int
	logger      logging.Logger
}

func NewRMQTestHandler(tester *usecase.RMQTester, limiter *ratelimit.Limiter, limitPerMin int, logger logging.Logger) *RMQTestHandler {
	if limitPerMin <= 0 {
		limitPerMin = 10 // §27.8: дефолт 10 запросов/мин на пользователя
	}
	return &RMQTestHandler{tester: tester, limiter: limiter, limitPerMin: limitPerMin, logger: logger}
}

// testRMQRequest — тело POST /api/nodes/test-rmq.
type testRMQRequest struct {
	Host     string `json:"host" binding:"required,max=253"`
	Port     int    `json:"port" binding:"omitempty,min=1,max=65535"`
	VHost    string `json:"vhost" binding:"omitempty,max=255"`
	User     string `json:"user" binding:"omitempty,max=255"`
	Password string `json:"password" binding:"omitempty,max=1024"`
	Queue    string `json:"queue" binding:"required,max=255"`
	UseTLS   bool   `json:"use_tls"`
}

// TestRMQ godoc
// @Summary  Проверить подключение к RabbitMQ (§27.8).
// @Description  Реальный AMQP-handshake: TCP-connect, auth, passive queue.declare. Не создаёт очередь и не забирает сообщения. Всегда 200; OK=false при любом проваленном шаге (диагностика, не функциональный вызов). Доступно manager+, rate-limit 10/мин на пользователя.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  testRMQRequest  true  "RabbitMQ connection params"
// @Success  200  {object}  usecase.RMQTestResult
// @Failure  400  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/nodes/test-rmq [post]
func (h *RMQTestHandler) TestRMQ(c *gin.Context) {
	if h.tester == nil {
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
		return
	}

	// Rate-limit по пользователю сессии (fail-open при недоступности Redis).
	key := "test-rmq:anon"
	if s, ok := sessionFromCtx(c); ok {
		key = "test-rmq:" + s.UserID
	}
	if ok, _ := h.limiter.Allow(c.Request.Context(), key, h.limitPerMin); !ok {
		localizedError(c, http.StatusTooManyRequests, "rmq.test_rate_limited")
		return
	}

	var req testRMQRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	res := h.tester.Test(c.Request.Context(), usecase.RMQProbeParams{
		Host:     req.Host,
		Port:     req.Port,
		VHost:    req.VHost,
		User:     req.User,
		Password: req.Password,
		Queue:    req.Queue,
		UseTLS:   req.UseTLS,
	})
	c.JSON(http.StatusOK, res)
}
