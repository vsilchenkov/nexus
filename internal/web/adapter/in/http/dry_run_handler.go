package http

import (
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"bus/internal/platform/logging"
	"bus/internal/web/usecase"
)

// DryRunHandler — POST /api/nodes/dry-run (§7.5.1 ТЗ).
//
// Принимает несохранённый конфиг узла (из формы) и synthetic-запрос.
// Возвращает пошаговый отчёт без побочных эффектов в production-логах.
type DryRunHandler struct {
	uc     *usecase.DryRunUsecase
	logger logging.Logger
}

func NewDryRunHandler(uc *usecase.DryRunUsecase, logger logging.Logger) *DryRunHandler {
	return &DryRunHandler{uc: uc, logger: logger}
}

// DryRunRequest — body POST /api/nodes/dry-run.
type DryRunRequest struct {
	Node    CreateNodeRequest `json:"node" binding:"required"`
	Request DryRunSubrequest  `json:"request" binding:"required"`
	UseMock *bool             `json:"use_mock"`
}

// DryRunSubrequest — параметры синтетического запроса, который пользователь
// «отправляет» через шину для проверки конфигурации (§7.5.1).
type DryRunSubrequest struct {
	Method  string              `json:"method" binding:"omitempty,oneof=GET POST PUT DELETE PATCH"`
	Query   map[string][]string `json:"query"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

// Run godoc
// @Summary  Dry-run: проверить конфиг узла без сохранения (§7.5.1).
// @Description  Прогоняет synthetic-запрос через pipeline шины и возвращает пошаговый отчёт.
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  DryRunRequest  true  "node + sub-request"
// @Success  200   {object}  usecase.DryRunReport
// @Failure  400   {object}  map[string]string
// @Security CookieAuth
// @Router   /api/nodes/dry-run [post]
func (h *DryRunHandler) Run(c *gin.Context) {
	var req DryRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	useMock := true
	if req.UseMock != nil {
		useMock = *req.UseMock
	}
	method := req.Request.Method
	if method == "" {
		method = http.MethodPost
	}

	headers := http.Header{}
	for k, vv := range req.Request.Headers {
		for _, v := range vv {
			headers.Add(k, v)
		}
	}
	query := url.Values{}
	for k, vv := range req.Request.Query {
		for _, v := range vv {
			query.Add(k, v)
		}
	}

	rep, err := h.uc.Run(c.Request.Context(), actorFromCtx(c), usecase.DryRunRequest{
		Node:    reqToDomain(req.Node),
		Method:  method,
		Query:   query,
		Headers: headers,
		Body:    []byte(req.Request.Body),
		UseMock: useMock,
	})
	if err != nil {
		if isValidationError(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h.logger.ErrorWithOp("dry-run failed", err, "node.dry_run",
			h.logger.Str("path", req.Node.Path))
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	c.JSON(http.StatusOK, rep)
}
