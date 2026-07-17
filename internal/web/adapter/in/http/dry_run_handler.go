package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// nodeCredsLoader — сохранённый узел для подмешивания кредов (§55.6).
// Из NodeUsecase здесь нужен ровно один метод (ISP).
type nodeCredsLoader interface {
	Get(ctx context.Context, id, teamID string) (*domain.Node, error)
}

// DryRunHandler — POST /api/nodes/dry-run (§7.5.1 ТЗ).
//
// Принимает несохранённый конфиг узла (из формы) и synthetic-запрос.
// Возвращает пошаговый отчёт без побочных эффектов в production-логах.
type DryRunHandler struct {
	uc *usecase.DryRunUsecase
	// nodes — источник сохранённых кредов при node_id (§55.6). nil допустим:
	// тогда работает только конфиг из тела.
	nodes  nodeCredsLoader
	logger logging.Logger
}

func NewDryRunHandler(uc *usecase.DryRunUsecase, nodes nodeCredsLoader, logger logging.Logger) *DryRunHandler {
	return &DryRunHandler{uc: uc, nodes: nodes, logger: logger}
}

// DryRunRequest — body POST /api/nodes/dry-run.
type DryRunRequest struct {
	Node CreateNodeRequest `json:"node" binding:"required"`
	// NodeID — id сохранённого узла, если тест запускают для существующего
	// конфига (кнопка «Тестовый запрос» на странице узла, §55.6). Нужен из-за
	// кредов: наружу они не отдаются никогда (nodeToResponse даёт лишь
	// *_credentials_set), поэтому без подмешивания реальный вызов ушёл бы с
	// пустым `Authorization` и вернул 401 — оператор решил бы, что сломана
	// авторизация узла. Пусто → конфиг берётся только из тела (создание узла).
	NodeID  string           `json:"node_id" binding:"omitempty,uuid"`
	Request DryRunSubrequest `json:"request" binding:"required"`
	UseMock *bool            `json:"use_mock"`
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
// @Summary  Dry-run: проверить конфиг узла без сохранения (§7.5.1, §55).
// @Description  Прогоняет synthetic-запрос через pipeline шины и возвращает пошаговый отчёт. use_mock=false (§55) — реальный вызов target через Sender: побочки нет (не пишет в ClickHouse, не влияет на метрики, статус узла и circuit breaker). node_id — подмешать креды сохранённого узла (пустые креды в body = оставить старые, §5.5).
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  DryRunRequest  true  "node + sub-request"
// @Success  200   {object}  usecase.DryRunReport
// @Failure  400   {object}  ErrorResponse
// @Failure  404   {object}  ErrorResponse  "node_id not found in current team"
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

	// Узел в dry-run не сохраняется в БД — но всё равно ставим TeamID из
	// текущей сессии (multi-tenancy v2): writeAudit пишет node.ID, узел
	// формы должен наследовать scope создателя.
	dryNode := reqToDomain(req.Node)
	dryNode.TeamID = currentTeamID(c)
	if err := h.mergeStoredCreds(c, &req, dryNode); err != nil {
		h.replyNodeLoadError(c, err)
		return
	}
	rep, err := h.uc.Run(c.Request.Context(), actorFromCtx(c), usecase.DryRunRequest{
		Node:    dryNode,
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

// mergeStoredCreds подмешивает в конфиг из формы креды сохранённого узла по
// конвенции §5.5 «пустые креды в запросе = оставить старое значение» — та же
// семантика, что у NodeHandler.Update. Без этого тест сохранённого узла всегда
// уходил бы без авторизации: наружу креды не отдаются (§55.6).
//
// Скоуп берётся из сессии (currentTeamID), поэтому чужой узел подтянуть нельзя:
// Get вернёт ErrNodeNotFound. Оператор при этом может направить креды на
// произвольный target — риск принят (§55.6): manager+ и так может изменить
// target_url узла, оставив креды прежними, и пустить туда боевой трафик. След
// остаётся в аудите.
func (h *DryRunHandler) mergeStoredCreds(c *gin.Context, req *DryRunRequest, dryNode *domain.Node) error {
	if req.NodeID == "" || h.nodes == nil {
		return nil
	}
	existing, err := h.nodes.Get(c.Request.Context(), req.NodeID, currentTeamID(c))
	if err != nil {
		return err
	}
	dryNode.ID = existing.ID
	if req.Node.AuthCredentials == "" {
		dryNode.AuthCredentials = existing.AuthCredentials
	}
	if req.Node.IncomingAuthCreds == "" {
		dryNode.IncomingAuthCredentials = existing.IncomingAuthCredentials
	}
	if req.Node.RMQPassword == "" {
		dryNode.RMQPassword = existing.RMQPassword
	}
	h.logger.Debug("dry-run: stored credentials merged",
		h.logger.Str("node_id", existing.ID),
		h.logger.Any("auth", req.Node.AuthCredentials == "" && existing.AuthCredentials != ""),
		h.logger.Any("incoming_auth", req.Node.IncomingAuthCreds == "" && existing.IncomingAuthCredentials != ""))
	return nil
}

func (h *DryRunHandler) replyNodeLoadError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrNodeNotFound) {
		localizedError(c, http.StatusNotFound, "node.not_found")
		return
	}
	h.logger.ErrorWithOp("dry-run: load node failed", err, "node.dry_run")
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
