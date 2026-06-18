package http

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// AsyncQueueHandler — управление async-очередью Kafka на узле requestAsync (§34.4).
// Admin-only (регистрируется в authedAdmin), мутации под CSRF (глобально на /api).
type AsyncQueueHandler struct {
	uc     *usecase.AsyncQueueUsecase
	logger logging.Logger
}

func NewAsyncQueueHandler(uc *usecase.AsyncQueueUsecase, logger logging.Logger) *AsyncQueueHandler {
	return &AsyncQueueHandler{uc: uc, logger: logger}
}

type queueMessageDTO struct {
	ID         string    `json:"id"`
	Partition  int       `json:"partition"`
	Offset     int64     `json:"offset"`
	Method     string    `json:"method"`
	TargetURL  string    `json:"target_url"`
	ReceivedAt time.Time `json:"received_at"`
	BodySize   int       `json:"body_size"`
}

type queueListDTO struct {
	Items          []queueMessageDTO `json:"items"`
	Capped         bool              `json:"capped"`
	KafkaAvailable bool              `json:"kafka_available"`
}

type queueBodyDTO struct {
	ID        string            `json:"id"`
	Method    string            `json:"method"`
	TargetURL string            `json:"target_url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body"`
}

type queuePurgeDTO struct {
	Cancelled      int  `json:"cancelled"`
	Capped         bool `json:"capped"`
	KafkaAvailable bool `json:"kafka_available"`
}

type queuePurgeRequest struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func toQueueMessageDTO(m port.QueueMessageMeta) queueMessageDTO {
	return queueMessageDTO{
		ID: m.ID, Partition: m.Partition, Offset: m.Offset, Method: m.Method,
		TargetURL: m.TargetURL, ReceivedAt: m.ReceivedAt, BodySize: m.BodySize,
	}
}

// List godoc
// @Summary  Первые 50 сообщений async-очереди узла (§34.4).
// @Description  Метаданные сообщений (без тела, как в логах). Тело тянется лениво через .../messages/body. Admin-only.
// @Tags     async-queue
// @Produce  json
// @Param    id  path  string  true  "node id"
// @Success  200  {object}  queueListDTO
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/async-queue/messages [get]
func (h *AsyncQueueHandler) List(c *gin.Context) {
	r, err := h.uc.List(c.Request.Context(), c.Param("id"), currentTeamID(c))
	if err != nil {
		h.queueError(c, err, "async_queue.list")
		return
	}
	items := make([]queueMessageDTO, 0, len(r.Items))
	for _, m := range r.Items {
		items = append(items, toQueueMessageDTO(m))
	}
	c.JSON(http.StatusOK, queueListDTO{Items: items, Capped: r.Capped, KafkaAvailable: r.KafkaAvailable})
}

// Body godoc
// @Summary  Тело одного сообщения async-очереди (§34.4).
// @Description  Ленивая подгрузка тела по физической координате (partition, offset) из списка. Admin-only.
// @Tags     async-queue
// @Produce  json
// @Param    id         path   string  true   "node id"
// @Param    partition  query  int     true   "partition"
// @Param    offset     query  int     true   "offset"
// @Success  200  {object}  queueBodyDTO
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse  "kafka unavailable"
// @Security CookieAuth
// @Router   /api/nodes/{id}/async-queue/messages/body [get]
func (h *AsyncQueueHandler) Body(c *gin.Context) {
	offset, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "offset required (int)"})
		return
	}
	partition, err := strconv.Atoi(c.Query("partition"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "partition required (int)"})
		return
	}
	body, err := h.uc.Body(c.Request.Context(), c.Param("id"), currentTeamID(c), partition, offset)
	if err != nil {
		h.queueError(c, err, "async_queue.body")
		return
	}
	c.JSON(http.StatusOK, queueBodyDTO{
		ID: body.ID, Method: body.Method, TargetURL: body.TargetURL,
		Headers: body.Headers, Body: string(body.Body),
	})
}

// DeleteOne godoc
// @Summary  Удалить (отменить) одно сообщение очереди (§34.4).
// @Description  Логическое удаление через cancel-set: Sender пропустит сообщение при доставке. Admin-only.
// @Tags     async-queue
// @Produce  json
// @Param    id     path  string  true  "node id"
// @Param    msgId  path  string  true  "message id (Envelope.ID)"
// @Success  204
// @Failure  404  {object}  ErrorResponse
// @Failure  503  {object}  ErrorResponse  "queue management unavailable"
// @Security CookieAuth
// @Router   /api/nodes/{id}/async-queue/messages/{msgId} [delete]
func (h *AsyncQueueHandler) DeleteOne(c *gin.Context) {
	err := h.uc.DeleteOne(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c), c.Param("msgId"))
	if err != nil {
		h.queueError(c, err, "async_queue.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Purge godoc
// @Summary  Очистить async-очередь узла за период или целиком (§34.4).
// @Description  Отменяет сообщения с ReceivedAt ∈ [from,to] (логически, через cancel-set). Пустые from/to = очистить всё. Admin-only.
// @Tags     async-queue
// @Accept   json
// @Produce  json
// @Param    id    path  string             true   "node id"
// @Param    body  body  queuePurgeRequest  false  "период (пусто = всё)"
// @Success  200  {object}  queuePurgeDTO
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/nodes/{id}/async-queue/purge [post]
func (h *AsyncQueueHandler) Purge(c *gin.Context) {
	var req queuePurgeRequest
	// Тело опционально: пустой запрос = очистить всё.
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if !req.From.IsZero() && !req.To.IsZero() && req.From.After(req.To) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "from must be before to"})
		return
	}
	r, err := h.uc.PurgePeriod(c.Request.Context(), actorFromCtx(c), c.Param("id"), currentTeamID(c), req.From, req.To)
	if err != nil {
		h.queueError(c, err, "async_queue.purge")
		return
	}
	c.JSON(http.StatusOK, queuePurgeDTO{Cancelled: r.Cancelled, Capped: r.Capped, KafkaAvailable: r.KafkaAvailable})
}

// queueError — единый маппинг ошибок управления очередью.
func (h *AsyncQueueHandler) queueError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, usecase.ErrAsyncQueueUnavailable):
		localizedError(c, http.StatusServiceUnavailable, "error.internal")
	case errors.Is(err, domain.ErrNodeNotFound):
		localizedError(c, http.StatusNotFound, "node.not_found")
	case errors.Is(err, domain.ErrNotFound):
		localizedError(c, http.StatusNotFound, "error.not_found")
	default:
		h.logger.ErrorWithOp("async queue op failed", err, op)
		localizedError(c, http.StatusInternalServerError, "error.internal")
	}
}
