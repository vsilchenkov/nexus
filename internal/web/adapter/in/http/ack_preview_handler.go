package http

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
)

// AckPreviewHandler — предпросмотр ответа приёма (§83). manager+
// (регистрируется в authedManager).
type AckPreviewHandler struct {
	uc     *usecase.AckPreviewUsecase
	logger logging.Logger
}

func NewAckPreviewHandler(uc *usecase.AckPreviewUsecase, logger logging.Logger) *AckPreviewHandler {
	return &AckPreviewHandler{uc: uc, logger: logger}
}

// ackPreviewSampleMax — потолок образца тела в предпросмотре. Рендер всё равно
// не читает больше ackspec.MaxParseBodyBytes; ограничение здесь — чтобы форма
// не отправляла мегабайты, которые заведомо не будут разобраны.
const ackPreviewSampleMax = 256 << 10

// AckPreviewRequest — тело POST /api/nodes/ack-preview.
//
// Проверяется СПЕКА, а не id узла: кнопка «Проверить» должна работать в форме
// ещё не сохранённого узла (та же линия, что у /api/ch-tables/verify, §64).
type AckPreviewRequest struct {
	Spec        *ackspec.Spec       `json:"spec" binding:"required"`
	SampleBody  string              `json:"sample_body" binding:"omitempty,max=262144"`
	SampleQuery map[string][]string `json:"sample_query"`
	SamplePath  string              `json:"sample_path" binding:"omitempty,max=1024"`
	ContentType string              `json:"content_type" binding:"omitempty,max=255"`
}

// Preview godoc
// @Summary  Предпросмотр ответа приёма по шаблону узла (§83).
// @Description  Прогоняет шаблон на образце запроса и возвращает тело, которое получил бы клиент. Ничего не сохраняет и никуда не ходит: работает по присланной спеке, поэтому доступен в форме ещё не сохранённого узла. Провал подстановки — не ошибка запроса: приходит 200 с ok=false, reason и проблемной подстановкой. 400 отдаётся только на невалидной спеке (её правит оператор в форме).
// @Tags     nodes
// @Accept   json
// @Produce  json
// @Param    body  body  AckPreviewRequest  true  "спека и образец запроса"
// @Success  200  {object}  usecase.AckPreviewReport
// @Failure  400  {object}  map[string]interface{}  "{error, code, field} — спека или шаблон невалидны"
// @Failure  403  {object}  map[string]interface{}  "нужна роль manager или admin"
// @Router   /api/nodes/ack-preview [post]
func (h *AckPreviewHandler) Preview(c *gin.Context) {
	var req AckPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.SampleBody) > ackPreviewSampleMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sample body is too large"})
		return
	}

	rep, err := h.uc.Run(c.Request.Context(), usecase.AckPreviewRequest{
		Spec:        req.Spec,
		SampleBody:  []byte(req.SampleBody),
		SampleQuery: url.Values(req.SampleQuery),
		SamplePath:  req.SamplePath,
		ContentType: req.ContentType,
	})
	if err != nil {
		h.replySpecError(c, err)
		return
	}
	c.JSON(http.StatusOK, rep)
}

// replySpecError отдаёт ошибку спеки в том же формате, что и сохранение узла
// ({error, code, field}) — форма подсвечивает то же поле и показывает тот же
// перевод, независимо от того, нажал оператор «Проверить» или «Сохранить».
func (h *AckPreviewHandler) replySpecError(c *gin.Context, err error) {
	lang := i18n.FromGin(c)
	switch {
	case errors.Is(err, ackspec.ErrTemplateSyntax), errors.Is(err, ackspec.ErrSpecInvalid):
		code, field, _ := nodeValidationCode(err)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": i18n.Translate(lang, code),
			"code":  code,
			"field": field,
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}
