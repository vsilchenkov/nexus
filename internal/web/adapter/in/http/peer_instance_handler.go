package http

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/i18n"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/ratelimit"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// defaultProbeRateLimitPerMin — потолок проверок на пользователя, если конфиг
// не задан. Проба — исходящий запрос с сервера, и зажатая кнопка «Проверить»
// не должна превращаться в обстрел соседа (тот же приём, что у §27.8).
const defaultProbeRateLimitPerMin = 30

// PeerInstanceHandler — реестр соседних инстансов Nexus (§73).
//
// Все маршруты admin-only: адрес, по которому сервер выполняет исходящий
// запрос, задаёт администратор, и понижать эту планку нельзя (см. границы
// защиты в §73 ТЗ).
type PeerInstanceHandler struct {
	uc          *usecase.PeerInstanceUsecase
	limiter     *ratelimit.Limiter
	limitPerMin int
	logger      logging.Logger
}

func NewPeerInstanceHandler(
	uc *usecase.PeerInstanceUsecase,
	limiter *ratelimit.Limiter,
	limitPerMin int,
	logger logging.Logger,
) *PeerInstanceHandler {
	if limitPerMin <= 0 {
		limitPerMin = defaultProbeRateLimitPerMin
	}
	return &PeerInstanceHandler{uc: uc, limiter: limiter, limitPerMin: limitPerMin, logger: logger}
}

// peerInstanceRequest — тело POST/PATCH /api/instances.
type peerInstanceRequest struct {
	Title   string `json:"title" binding:"required,max=64"`
	BaseURL string `json:"base_url" binding:"required,max=255"`
	Comment string `json:"comment" binding:"omitempty,max=255"`
}

// probeRequest — тело POST /api/instances/probe: проверка адреса до сохранения.
type probeRequest struct {
	BaseURL string `json:"base_url" binding:"required,max=255"`
}

// PeerInstanceResponse — запись реестра с кешем последней пробы.
type PeerInstanceResponse struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	BaseURL string `json:"base_url"`
	Comment string `json:"comment"`

	LastStatus string `json:"last_status"`
	// LastVersion — версия Web Service соседа: Receiver и Sender версию наружу
	// не отдают, поэтому «версия инстанса» — это всегда версия его Web.
	LastVersion    string     `json:"last_version"`
	LastInstanceID string     `json:"last_instance_id"`
	LastLatencyMS  *int       `json:"last_latency_ms"`
	LastError      string     `json:"last_error"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`

	CreatedBy string    `json:"created_by"`
	UpdatedBy string    `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListPeerInstancesResponse — ответ GET /api/instances и обеих ручек проверки.
type ListPeerInstancesResponse struct {
	Items []PeerInstanceResponse `json:"items"`
}

// ProbeInstanceResponse — результат пробы произвольного адреса.
type ProbeInstanceResponse struct {
	Status     string `json:"status"`
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
	LatencyMS  *int   `json:"latency_ms"`
	// Error — короткая нормализованная причина ("timeout", "http 502"), без
	// сырого тела ответа соседа.
	Error string `json:"error"`
}

func peerInstanceToResponse(p *domain.PeerInstance) PeerInstanceResponse {
	return PeerInstanceResponse{
		ID: p.ID, Title: p.Title, BaseURL: p.BaseURL, Comment: p.Comment,
		LastStatus: string(p.LastStatus), LastVersion: p.LastVersion,
		LastInstanceID: p.LastInstanceID, LastLatencyMS: p.LastLatencyMS,
		LastError: p.LastError, LastCheckedAt: p.LastCheckedAt,
		CreatedBy: p.CreatedBy, UpdatedBy: p.UpdatedBy,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func peerInstancesToResponse(items []*domain.PeerInstance) ListPeerInstancesResponse {
	out := ListPeerInstancesResponse{Items: make([]PeerInstanceResponse, 0, len(items))}
	for _, p := range items {
		out.Items = append(out.Items, peerInstanceToResponse(p))
	}
	return out
}

// List godoc
// @Summary  Реестр соседних инстансов (§73).
// @Description  Список подключённых развёртываний Nexus с кешем последней проверки. Сетевых запросов не делает — отдаёт сохранённые значения, чтобы таблица рисовалась мгновенно. Свой инстанс в список не входит. Admin-only.
// @Tags     instances
// @Produce  json
// @Success  200  {object}  ListPeerInstancesResponse
// @Failure  500  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/instances [get]
func (h *PeerInstanceHandler) List(c *gin.Context) {
	items, err := h.uc.List(c.Request.Context())
	if err != nil {
		h.replyDomainError(c, err, "instance.list")
		return
	}
	c.JSON(http.StatusOK, peerInstancesToResponse(items))
}

// Create godoc
// @Summary  Подключить инстанс (§73).
// @Description  Адрес нормализуется (обрезаются пробелы и хвостовой слеш, хост — в нижний регистр) и обязан быть origin'ом http(s) без пути, query и учётных данных. Admin-only.
// @Tags     instances
// @Accept   json
// @Produce  json
// @Param    body  body  peerInstanceRequest  true  "Instance"
// @Success  201  {object}  PeerInstanceResponse
// @Failure  400  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse  "адрес уже в реестре"
// @Security CookieAuth
// @Router   /api/instances [post]
func (h *PeerInstanceHandler) Create(c *gin.Context) {
	var req peerInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	p := &domain.PeerInstance{Title: req.Title, BaseURL: req.BaseURL, Comment: req.Comment}
	if err := h.uc.Create(c.Request.Context(), actorFromCtx(c), p); err != nil {
		h.replyDomainError(c, err, "instance.create")
		return
	}
	c.JSON(http.StatusCreated, peerInstanceToResponse(p))
}

// Update godoc
// @Summary  Изменить инстанс реестра (§73).
// @Description  Меняет название, адрес и комментарий. Кеш последней проверки не сбрасывается: пустой статус читался бы как «не отвечает», хотя проверки просто ещё не было. Admin-only.
// @Tags     instances
// @Accept   json
// @Produce  json
// @Param    id    path  string               true  "Instance ID"
// @Param    body  body  peerInstanceRequest  true  "Instance"
// @Success  200  {object}  PeerInstanceResponse
// @Failure  400  {object}  ErrorResponse
// @Failure  404  {object}  ErrorResponse
// @Failure  409  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/instances/{id} [patch]
func (h *PeerInstanceHandler) Update(c *gin.Context) {
	var req peerInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := h.uc.Update(c.Request.Context(), actorFromCtx(c), c.Param("id"), req.Title, req.BaseURL, req.Comment)
	if err != nil {
		h.replyDomainError(c, err, "instance.update")
		return
	}
	c.JSON(http.StatusOK, peerInstanceToResponse(updated))
}

// Delete godoc
// @Summary  Отключить инстанс (§73).
// @Description  Удаляет запись реестра. На соседний инстанс не влияет — реестр хранит только ссылку. Admin-only.
// @Tags     instances
// @Produce  json
// @Param    id  path  string  true  "Instance ID"
// @Success  204  "no content"
// @Failure  404  {object}  ErrorResponse
// @Security CookieAuth
// @Router   /api/instances/{id} [delete]
func (h *PeerInstanceHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), actorFromCtx(c), c.Param("id")); err != nil {
		h.replyDomainError(c, err, "instance.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// CheckAll godoc
// @Summary  Проверить все инстансы реестра (§73).
// @Description  Опрашивает публичные GET /api/version и GET /ready каждого соседа с сервера (не из браузера: CORS не настроен, CSP задаёт connect-src 'self'). Недоступность соседа — не ошибка запроса, а его статус. Admin-only, rate-limit на пользователя.
// @Tags     instances
// @Produce  json
// @Success  200  {object}  ListPeerInstancesResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/instances/check [post]
func (h *PeerInstanceHandler) CheckAll(c *gin.Context) {
	if !h.allowProbe(c) {
		return
	}
	items, err := h.uc.CheckAll(c.Request.Context())
	if err != nil {
		h.replyDomainError(c, err, "instance.check_all")
		return
	}
	c.JSON(http.StatusOK, peerInstancesToResponse(items))
}

// CheckOne godoc
// @Summary  Проверить один инстанс реестра (§73).
// @Tags     instances
// @Produce  json
// @Param    id  path  string  true  "Instance ID"
// @Success  200  {object}  PeerInstanceResponse
// @Failure  404  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/instances/{id}/check [post]
func (h *PeerInstanceHandler) CheckOne(c *gin.Context) {
	if !h.allowProbe(c) {
		return
	}
	p, err := h.uc.CheckOne(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.replyDomainError(c, err, "instance.check_one")
		return
	}
	c.JSON(http.StatusOK, peerInstanceToResponse(p))
}

// Probe godoc
// @Summary  Проверить адрес до сохранения (§73).
// @Description  Диалог подключения показывает версию и код инстанса ещё до создания записи. Ничего не сохраняет. Admin-only, rate-limit на пользователя.
// @Tags     instances
// @Accept   json
// @Produce  json
// @Param    body  body  probeRequest  true  "Address"
// @Success  200  {object}  ProbeInstanceResponse
// @Failure  400  {object}  ErrorResponse
// @Failure  429  {object}  ErrorResponse  "rate limit exceeded"
// @Security CookieAuth
// @Router   /api/instances/probe [post]
func (h *PeerInstanceHandler) Probe(c *gin.Context) {
	if !h.allowProbe(c) {
		return
	}
	var req probeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	res, err := h.uc.Probe(c.Request.Context(), req.BaseURL)
	if err != nil {
		h.replyDomainError(c, err, "instance.probe")
		return
	}
	c.JSON(http.StatusOK, probeToResponse(res))
}

func probeToResponse(res port.InstanceProbeResult) ProbeInstanceResponse {
	return ProbeInstanceResponse{
		Status:     string(res.Status),
		Version:    res.Version,
		InstanceID: res.InstanceID,
		LatencyMS:  res.LatencyMS,
		Error:      res.Error,
	}
}

// allowProbe проверяет квоту на исходящие проверки. Возвращает false и уже
// отправленный 429, если квота исчерпана.
//
// Лимитер fail-open при недоступности Redis: потеря защиты от частых нажатий
// менее вредна, чем неработающая вкладка при сбое кеша (тот же выбор, что в
// §27.8 и на входе логина).
func (h *PeerInstanceHandler) allowProbe(c *gin.Context) bool {
	if h.limiter == nil {
		return true
	}
	key := "instance-probe:anon"
	if s, ok := sessionFromCtx(c); ok {
		key = "instance-probe:" + s.UserID
	}
	if ok, _ := h.limiter.Allow(c.Request.Context(), key, h.limitPerMin); !ok {
		h.replyCode(c, http.StatusTooManyRequests, "instance.probe_rate_limited")
		return false
	}
	return true
}

func (h *PeerInstanceHandler) replyDomainError(c *gin.Context, err error, op string) {
	switch {
	case errors.Is(err, domain.ErrPeerInstanceURLInvalid):
		h.replyCode(c, http.StatusBadRequest, "instance.url_invalid")
	case errors.Is(err, domain.ErrPeerInstanceTitleLength):
		h.replyCode(c, http.StatusBadRequest, "instance.title_length")
	case errors.Is(err, domain.ErrPeerInstanceCommentLength):
		h.replyCode(c, http.StatusBadRequest, "instance.comment_length")
	case errors.Is(err, domain.ErrPeerInstanceStatusInvalid):
		h.replyCode(c, http.StatusBadRequest, "instance.status_invalid")
	case errors.Is(err, domain.ErrPeerInstanceNotFound):
		h.replyCode(c, http.StatusNotFound, "instance.not_found")
	case errors.Is(err, domain.ErrPeerInstanceAlreadyExists):
		h.replyCode(c, http.StatusConflict, "instance.already_exists")
	default:
		h.replyServerError(c, err, op)
	}
}

func (h *PeerInstanceHandler) replyCode(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": i18n.Translate(i18n.FromGin(c), code), "code": code})
}

func (h *PeerInstanceHandler) replyServerError(c *gin.Context, err error, op string) {
	h.logger.ErrorWithOp("peer instance handler error", err, op,
		h.logger.Str("path", c.Request.URL.Path))
	localizedError(c, http.StatusInternalServerError, "error.internal")
}
