package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// auditReverter — то, что обработчику нужно от отмены строк журнала (§108).
type auditReverter interface {
	Preview(ctx context.Context, entryID string) (*usecase.RevertPreview, error)
	Apply(ctx context.Context, actor usecase.Actor, entryID string, fields []string, expected time.Time) error
}

// AuditRevertHandler — GET/POST /api/audit/revert/:id (admin, только UI-сессия,
// §108.7).
type AuditRevertHandler struct {
	uc     auditReverter
	logger logging.Logger
}

// NewAuditRevertHandler собирает обработчик отмены изменений из журнала.
func NewAuditRevertHandler(uc auditReverter, logger logging.Logger) *AuditRevertHandler {
	return &AuditRevertHandler{uc: uc, logger: logger}
}

// RevertPreviewResponse — предпросмотр отмены строки журнала (§108.7).
type RevertPreviewResponse struct {
	Entry RevertEntryDTO `json:"entry"`
	// Node — текущий узел; null — узел удалён.
	Node   *RevertNodeDTO   `json:"node"`
	State  string           `json:"state" example:"stale"`
	Moved  bool             `json:"moved"`
	Fields []RevertFieldDTO `json:"fields"`
	DryRun RevertDryRunDTO  `json:"dry_run"`
}

// RevertEntryDTO — отменяемая строка журнала.
type RevertEntryDTO struct {
	ID        string    `json:"id"`
	Action    string    `json:"action" example:"node.update"`
	CreatedAt time.Time `json:"created_at"`
	UserLogin string    `json:"user_login"`
}

// RevertNodeDTO — узел, к которому относится строка. UpdatedAt клиент обязан
// вернуть в expected_updated_at как пришло: Date в JavaScript отбрасывает
// микросекунды, и сравнение не сошлось бы никогда (§108.5).
type RevertNodeDTO struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	TeamID    string    `json:"team_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RevertFieldDTO — поле строки: «Было», «Стало», текущее значение и статус
// (actual/overwritten/already/inapplicable/unrecoverable/secret, §108.3).
// У кредов current = null: в журнале их значений нет.
type RevertFieldDTO struct {
	Name    string `json:"name"`
	Before  any    `json:"before"`
	After   any    `json:"after"`
	Current any    `json:"current"`
	Status  string `json:"status" example:"overwritten"`
	Warning string `json:"warning" example:"credentials"`
}

// RevertDryRunDTO — чем закончилась бы отмена выбора по умолчанию. Пустой
// code — применится (или выбирать нечего). Error — перевод code на языке
// запроса, запасной текст для клиента.
type RevertDryRunDTO struct {
	Code    string   `json:"code"`
	Field   string   `json:"field"`
	NotHeld []string `json:"not_held"`
	Error   string   `json:"error,omitempty"`
}

// RevertApplyRequest — тело POST /api/audit/revert/:id.
type RevertApplyRequest struct {
	Fields []string `json:"fields" binding:"required"`
	// ExpectedUpdatedAt — node.updated_at из предпросмотра, строкой как пришла.
	ExpectedUpdatedAt string `json:"expected_updated_at" binding:"required"`
}

// Preview godoc
// @Summary  Предпросмотр отмены изменения узла из журнала (admin, §108).
// @Description  Строка node.update/node.revert против текущего узла: статус каждого поля, состояние строки и пробный прогон отмены выбора по умолчанию. node = null — узел удалён. Недоступно по API-токену.
// @Tags     audit
// @Produce  json
// @Param    id   path  string  true  "id строки журнала"
// @Success  200  {object}  RevertPreviewResponse
// @Failure  400  {object}  ErrorResponse  "audit.revert.not_revertible"
// @Failure  403  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse  "audit.revert.entry_not_found"
// @Security CookieAuth
// @Router   /api/audit/revert/{id} [get]
func (h *AuditRevertHandler) Preview(c *gin.Context) {
	p, err := h.uc.Preview(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyError(c, err, "audit.revert.preview")
		return
	}
	c.JSON(http.StatusOK, h.previewResponse(c, p))
}

// Apply godoc
// @Summary  Отменить изменение узла из журнала (admin, §108).
// @Description  Возвращает выбранным полям значения «Было» и пишет в журнал строку node.revert. expected_updated_at — updated_at узла из предпросмотра без преобразований; узел изменился — 409.
// @Tags     audit
// @Accept   json
// @Param    id    path  string              true  "id строки журнала"
// @Param    body  body  RevertApplyRequest  true  "поля и версия узла"
// @Success  204
// @Failure  400  {object}  ErrorResponse  "not_revertible / field_invalid / not_held / валидация узла"
// @Failure  403  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse  "entry_not_found / node_gone"
// @Failure  409  {object}  ErrorResponse  "conflict / путь занят"
// @Security CookieAuth
// @Router   /api/audit/revert/{id} [post]
func (h *AuditRevertHandler) Apply(c *gin.Context) {
	var req RevertApplyRequest
	// Текст ошибки разбора наружу не отдаём: encoding/json вклеивает в него
	// куски тела.
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	expected, err := time.Parse(time.RFC3339Nano, req.ExpectedUpdatedAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid expected_updated_at (expected RFC3339)"})
		return
	}
	err = h.uc.Apply(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.Fields, expected)
	if err != nil {
		h.replyError(c, err, "audit.revert.apply")
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AuditRevertHandler) previewResponse(c *gin.Context, p *usecase.RevertPreview) RevertPreviewResponse {
	e := p.Entry
	out := RevertPreviewResponse{
		Entry:  RevertEntryDTO{ID: e.ID, Action: e.Action, CreatedAt: e.CreatedAt, UserLogin: e.UserLogin},
		State:  string(p.State),
		Moved:  p.Moved,
		Fields: make([]RevertFieldDTO, 0, len(p.Fields)),
		DryRun: h.dryRun(c, p.DryRunErr),
	}
	if n := p.Node; n != nil {
		out.Node = &RevertNodeDTO{ID: n.ID, Path: n.Path, TeamID: n.TeamID, UpdatedAt: n.UpdatedAt}
	}
	for _, f := range p.Fields {
		out.Fields = append(out.Fields, RevertFieldDTO{
			Name: f.Name, Before: f.Before, After: f.After, Current: f.Current,
			Status: string(f.Status), Warning: f.Warning,
		})
	}
	return out
}

// dryRun переводит исход пробного прогона в DTO. Не доменная ошибка (сбой
// ClickHouse при проверке таблицы и т.п.) предпросмотр не роняет: код
// error.internal, причина — в лог.
func (h *AuditRevertHandler) dryRun(c *gin.Context, err error) RevertDryRunDTO {
	out := RevertDryRunDTO{NotHeld: []string{}}
	if err == nil {
		return out
	}
	r, ok := revertErrorReply(err)
	if !ok {
		h.logger.Warn("audit revert: preview dry run failed", h.logger.Err(err))
		r.code = "error.internal"
	}
	out.Code, out.Field, out.Error = r.code, r.field, i18n.Translate(i18n.FromGin(c), r.code)
	if r.notHeld != nil {
		out.NotHeld = r.notHeld
	}
	return out
}

func (h *AuditRevertHandler) replyError(c *gin.Context, err error, op string) {
	r, ok := revertErrorReply(err)
	if !ok {
		logServerError(c, h.logger, "audit revert failed", err, op)
		localizedError(c, http.StatusInternalServerError, "error.internal")
		return
	}
	h.logger.Debug("audit revert: rejected", h.logger.Str("op", op),
		h.logger.Str("code", r.code), h.logger.Int("status", r.status))
	body := gin.H{"error": i18n.Translate(i18n.FromGin(c), r.code), "code": r.code}
	if r.field != "" {
		body["field"] = r.field
	}
	if r.notHeld != nil {
		body["fields"] = r.notHeld
	}
	c.JSON(r.status, body)
}

// revertReply — HTTP-ответ на доменную ошибку отмены.
type revertReply struct {
	status  int
	code    string
	field   string
	notHeld []string
}

// revertErrorReply сопоставляет ошибку отмены ответу (§108.7). ok=false —
// ошибка не доменная, это 500.
func revertErrorReply(err error) (revertReply, bool) {
	var notHeld *usecase.RevertNotHeldError
	switch {
	case errors.Is(err, domain.ErrAuditEntryNotFound):
		return revertReply{status: http.StatusNotFound, code: "audit.revert.entry_not_found"}, true
	case errors.Is(err, domain.ErrAuditNotRevertible):
		return revertReply{status: http.StatusBadRequest, code: "audit.revert.not_revertible"}, true
	case errors.Is(err, domain.ErrAuditRevertFieldInvalid):
		return revertReply{status: http.StatusBadRequest, code: "audit.revert.field_invalid"}, true
	case errors.Is(err, domain.ErrAuditRevertConflict):
		return revertReply{status: http.StatusConflict, code: "audit.revert.conflict"}, true
	case errors.As(err, &notHeld):
		return revertReply{status: http.StatusBadRequest, code: "audit.revert.not_held", notHeld: notHeld.Fields}, true
	case errors.Is(err, domain.ErrNodeNotFound):
		return revertReply{status: http.StatusNotFound, code: "audit.revert.node_gone"}, true
	case errors.Is(err, domain.ErrNodeAlreadyExists):
		return revertReply{status: http.StatusConflict, code: "node.already_exists"}, true
	}
	if code, field, ok := nodeValidationCode(err); ok {
		return revertReply{status: http.StatusBadRequest, code: code, field: field}, true
	}
	return revertReply{}, false
}
