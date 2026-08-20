package http

import (
	"encoding/csv"
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

// Пределы выдачи журнала отказов.
const (
	rejectedDefaultLimit = 100
	rejectedMaxLimit     = 1000
	// rejectedExportLimit — потолок строк выгрузки. Журнал агрегированный, и
	// десять тысяч групп — это уже не «посмотреть, кто стучится», а выгрузка
	// всей истории; больше отдавать смысла нет.
	rejectedExportLimit = 10000
)

// RejectedHandler — журнал отказов на входе (§94.6).
type RejectedHandler struct {
	uc     *usecase.RejectedUsecase
	logger logging.Logger
}

func NewRejectedHandler(uc *usecase.RejectedUsecase, logger logging.Logger) *RejectedHandler {
	return &RejectedHandler{uc: uc, logger: logger}
}

// rejectedGroupResponse — группа отказов в ответе API.
type rejectedGroupResponse struct {
	ID         string     `json:"id"`
	TeamSlug   string     `json:"team_slug"`
	TeamID     string     `json:"team_id,omitempty"`
	TeamName   string     `json:"team_name,omitempty"`
	NodePath   string     `json:"node_path"`
	Reason     string     `json:"reason"`
	HTTPMethod string     `json:"http_method"`
	Status     int32      `json:"status"`
	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
	Count      int64      `json:"count"`
	Clients    int32      `json:"clients"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
}

// rejectedClientResponse — клиент группы.
type rejectedClientResponse struct {
	IP        string    `json:"ip"`
	Host      string    `json:"host,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
	Count     int64     `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// rejectedSampleResponse — сохранённый запрос группы. Тела здесь нет никогда,
// только его размер (§94.9).
type rejectedSampleResponse struct {
	At         time.Time         `json:"at"`
	ClientIP   string            `json:"client_ip"`
	HTTPMethod string            `json:"http_method"`
	RawPath    string            `json:"raw_path"`
	Status     int32             `json:"status"`
	BodyBytes  int64             `json:"body_bytes"`
	RequestID  string            `json:"request_id,omitempty"`
	Headers    map[string]string `json:"headers"`
}

// rejectedListResponse — ответ списка.
type rejectedListResponse struct {
	Groups []rejectedGroupResponse `json:"groups"`
	Total  int64                   `json:"total"`
	// RetentionDays/Collecting: без них интерфейс не отличит «отказов не было»
	// от «сбор выключен настройкой» (§94.5).
	RetentionDays int  `json:"retention_days"`
	Collecting    bool `json:"collecting"`
}

// rejectedDetailsResponse — карточка группы.
type rejectedDetailsResponse struct {
	Group      rejectedGroupResponse       `json:"group"`
	Clients    []rejectedClientResponse    `json:"clients"`
	Samples    []rejectedSampleResponse    `json:"samples"`
	Suggestion *rejectedSuggestionResponse `json:"suggestion,omitempty"`
}

// rejectedSuggestionResponse — подсказка «похоже, имелся в виду этот узел».
type rejectedSuggestionResponse struct {
	NodeID   string `json:"node_id"`
	Path     string `json:"path"`
	TeamSlug string `json:"team_slug"`
	Distance int    `json:"distance"`
}

// rejectedSummaryResponse — счётчики за период.
type rejectedSummaryResponse struct {
	Count      int64 `json:"count"`
	Groups     int64 `json:"groups"`
	Clients    int64 `json:"clients"`
	Unresolved int64 `json:"unresolved"`
	// RetentionDays/Collecting — то же, что в списке: строка на рабочем столе
	// не должна появляться, когда журнал выключен.
	RetentionDays int  `json:"retention_days"`
	Collecting    bool `json:"collecting"`
}

// List godoc
// @Summary  Журнал отказов на входе (§94.6).
// @Description  Группы отклонённых запросов: куда, почему, сколько раз, от скольких клиентов. Оператор и менеджер видят свою текущую команду, администратор — все и группы с неопознанным слогом.
// @Tags     rejected
// @Produce  json
// @Param    from              query  string  false  "нижняя граница last_seen (RFC3339)"
// @Param    to                query  string  false  "верхняя граница last_seen (RFC3339)"
// @Param    reasons           query  string  false  "коды причин через запятую (node_not_found, method_not_allowed, …)"
// @Param    team_slug         query  string  false  "фильтр по команде (только admin)"
// @Param    unknown_team      query  bool    false  "только группы с неопознанным слогом (только admin)"
// @Param    q                 query  string  false  "поиск по пути, слогу, IP, PTR-имени, User-Agent"
// @Param    include_resolved  query  bool    false  "включать разобранные группы"
// @Param    limit             query  int     false  "default 100, max 1000"
// @Param    offset            query  int     false  "смещение"
// @Success  200  {object}  rejectedListResponse
// @Failure  403  {object}  ErrorResponse  "роль ниже operator"
// @Router   /api/rejected [get]
func (h *RejectedHandler) List(c *gin.Context) {
	res, err := h.uc.List(c.Request.Context(), h.scope(c), rejectedFilterFromQuery(c, rejectedDefaultLimit, rejectedMaxLimit))
	if err != nil {
		h.fail(c, err, "rejected.list")
		return
	}
	out := rejectedListResponse{
		Groups:        make([]rejectedGroupResponse, 0, len(res.Groups)),
		Total:         res.Total,
		RetentionDays: res.RetentionDays,
		Collecting:    res.Collecting,
	}
	for _, g := range res.Groups {
		out.Groups = append(out.Groups, toRejectedGroupResponse(g))
	}
	c.JSON(http.StatusOK, out)
}

// Summary godoc
// @Summary  Счётчики отказов за период (§94.6).
// @Description  Число отказов, групп, разных клиентов и неразобранных групп — для строки на рабочем столе и бейджа раздела. Разобранные группы в счётчики входят: «сколько отказов за сутки» не должно меняться от нажатия «разобрано».
// @Tags     rejected
// @Produce  json
// @Param    from  query  string  false  "нижняя граница last_seen (RFC3339)"
// @Param    to    query  string  false  "верхняя граница last_seen (RFC3339)"
// @Success  200  {object}  rejectedSummaryResponse
// @Router   /api/rejected/summary [get]
func (h *RejectedHandler) Summary(c *gin.Context) {
	s, err := h.uc.Summary(c.Request.Context(), h.scope(c), rejectedFilterFromQuery(c, 0, 0))
	if err != nil {
		h.fail(c, err, "rejected.summary")
		return
	}
	c.JSON(http.StatusOK, rejectedSummaryResponse{
		Count:         s.Count,
		Groups:        s.Groups,
		Clients:       s.Clients,
		Unresolved:    s.Unresolved,
		RetentionDays: s.RetentionDays,
		Collecting:    s.Collecting,
	})
}

// Get godoc
// @Summary  Карточка группы отказов (§94.6).
// @Description  Группа с клиентами (IP, PTR-имя, User-Agent), последними запросами и подсказкой похожего узла. Чужая группа отдаётся как «не найдено»: иначе по коду ответа восстанавливались бы адреса других команд.
// @Tags     rejected
// @Produce  json
// @Param    id  path  string  true  "id группы"
// @Success  200  {object}  rejectedDetailsResponse
// @Failure  404  {object}  ErrorResponse  "группы нет либо она вне области видимости"
// @Router   /api/rejected/{id} [get]
func (h *RejectedHandler) Get(c *gin.Context) {
	d, err := h.uc.Details(c.Request.Context(), h.scope(c), c.Param("id"))
	if err != nil {
		h.fail(c, err, "rejected.get")
		return
	}
	out := rejectedDetailsResponse{
		Group:   toRejectedGroupResponse(d.Group),
		Clients: make([]rejectedClientResponse, 0, len(d.Clients)),
		Samples: make([]rejectedSampleResponse, 0, len(d.Samples)),
	}
	for _, cl := range d.Clients {
		out.Clients = append(out.Clients, rejectedClientResponse{
			IP: cl.IP, Host: cl.Host, UserAgent: cl.UserAgent,
			Count: cl.Count, FirstSeen: cl.FirstSeen, LastSeen: cl.LastSeen,
		})
	}
	for _, s := range d.Samples {
		out.Samples = append(out.Samples, rejectedSampleResponse{
			At: s.At, ClientIP: s.ClientIP, HTTPMethod: s.HTTPMethod, RawPath: s.RawPath,
			Status: s.Status, BodyBytes: s.BodyBytes, RequestID: s.RequestID, Headers: s.Headers,
		})
	}
	if d.Suggestion != nil {
		out.Suggestion = &rejectedSuggestionResponse{
			NodeID: d.Suggestion.NodeID, Path: d.Suggestion.Path,
			TeamSlug: d.Suggestion.TeamSlug, Distance: d.Suggestion.Distance,
		}
	}
	c.JSON(http.StatusOK, out)
}

// Resolve godoc
// @Summary  Пометить группу отказов разобранной (§94.6).
// @Description  Отметка снимается автоматически при новом отказе в эту группу — иначе однажды закрытая группа скрывала бы возобновившуюся проблему.
// @Tags     rejected
// @Param    id  path  string  true  "id группы"
// @Success  204  "разобрано"
// @Failure  404  {object}  ErrorResponse  "группы нет либо она вне области видимости"
// @Router   /api/rejected/{id}/resolve [post]
func (h *RejectedHandler) Resolve(c *gin.Context) {
	if err := h.uc.Resolve(c.Request.Context(), h.scope(c), actorFromCtx(c), c.Param("id")); err != nil {
		h.fail(c, err, "rejected.resolve")
		return
	}
	c.Status(http.StatusNoContent)
}

// Delete godoc
// @Summary  Удалить группу отказов (§94.6).
// @Description  Удаляет группу вместе с её клиентами и сохранёнными запросами. Записывается в журнал аудита.
// @Tags     rejected
// @Param    id  path  string  true  "id группы"
// @Success  204  "удалено"
// @Failure  404  {object}  ErrorResponse  "группы нет либо она вне области видимости"
// @Router   /api/rejected/{id} [delete]
func (h *RejectedHandler) Delete(c *gin.Context) {
	if err := h.uc.Delete(c.Request.Context(), h.scope(c), actorFromCtx(c), c.Param("id")); err != nil {
		h.fail(c, err, "rejected.delete")
		return
	}
	c.Status(http.StatusNoContent)
}

// Export godoc
// @Summary  Выгрузка журнала отказов в CSV (§94.6).
// @Description  Те же фильтры, что у списка. CSV в UTF-8 с BOM (для Excel), 10 колонок.
// @Tags     rejected
// @Produce  text/csv
// @Param    from              query  string  false  "нижняя граница last_seen (RFC3339)"
// @Param    to                query  string  false  "верхняя граница last_seen (RFC3339)"
// @Param    reasons           query  string  false  "коды причин через запятую"
// @Param    q                 query  string  false  "поиск"
// @Param    include_resolved  query  bool    false  "включать разобранные"
// @Success  200  {string}  string  "CSV"
// @Router   /api/rejected/export.csv [get]
func (h *RejectedHandler) Export(c *gin.Context) {
	f := rejectedFilterFromQuery(c, rejectedExportLimit, rejectedExportLimit)
	res, err := h.uc.List(c.Request.Context(), h.scope(c), f)
	if err != nil {
		h.fail(c, err, "rejected.export")
		return
	}

	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="nexus-rejected-`+
		time.Now().UTC().Format("20060102-150405")+`.csv"`)
	// BOM — чтобы Excel открыл кириллицу без ручного выбора кодировки (тот же
	// приём, что в выгрузке аудита).
	_, _ = c.Writer.WriteString("\ufeff")

	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	_ = w.Write([]string{
		"team_slug", "node_path", "reason", "http_method", "status",
		"count", "clients", "first_seen", "last_seen", "resolved_at",
	})
	for _, g := range res.Groups {
		resolved := ""
		if g.ResolvedAt != nil {
			resolved = g.ResolvedAt.Format(time.RFC3339)
		}
		_ = w.Write([]string{
			g.TeamSlug, g.NodePath, string(g.Reason), g.HTTPMethod, strconv.Itoa(int(g.Status)),
			strconv.FormatInt(g.Count, 10), strconv.Itoa(int(g.Clients)),
			g.FirstSeen.Format(time.RFC3339), g.LastSeen.Format(time.RFC3339), resolved,
		})
	}
}

// scope собирает область видимости вызывающего: команда сессии + признак
// администратора. Фильтры по команде и «неопознанные» действуют только у
// администратора — usecase их у остальных игнорирует.
func (h *RejectedHandler) scope(c *gin.Context) usecase.RejectedScope {
	return usecase.RejectedScope{
		TeamID:          currentTeamID(c),
		IsAdmin:         isAdminSession(c),
		TeamSlug:        c.Query("team_slug"),
		UnknownTeamOnly: c.Query("unknown_team") == "true",
	}
}

// fail отвечает на ошибку usecase: отсутствие группы — 404, остальное — 500.
func (h *RejectedHandler) fail(c *gin.Context, err error, op string) {
	if errors.Is(err, domain.ErrRejectedGroupNotFound) {
		c.JSON(http.StatusNotFound, ErrorResponse{Error: "rejected group not found"})
		return
	}
	h.logger.ErrorWithOp("rejected request failed", err, op)
	c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "internal error"})
}

// rejectedFilterFromQuery разбирает фильтры выдачи. Общая для списка, сводки и
// выгрузки: разъехавшиеся условия означали бы, что выгрузка содержит не то,
// что показано на экране.
func rejectedFilterFromQuery(c *gin.Context, defaultLimit, maxLimit int) port.RejectedFilter {
	f := port.RejectedFilter{
		Query:           c.Query("q"),
		IncludeResolved: c.Query("include_resolved") == "true",
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = t
		}
	}
	if v := c.Query("reasons"); v != "" {
		for _, r := range splitCSV(v) {
			reason := domain.RejectReason(r)
			// Неизвестный код молча отбрасывается: он не может ничего найти, а
			// подстановка его в запрос лишь удлиняла бы условие.
			if reason.Valid() {
				f.Reasons = append(f.Reasons, reason)
			}
		}
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if maxLimit > 0 && f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	return f
}

func toRejectedGroupResponse(g *domain.RejectedGroup) rejectedGroupResponse {
	return rejectedGroupResponse{
		ID: g.ID, TeamSlug: g.TeamSlug, TeamID: g.TeamID, TeamName: g.TeamName,
		NodePath: g.NodePath, Reason: string(g.Reason), HTTPMethod: g.HTTPMethod,
		Status: g.Status, FirstSeen: g.FirstSeen, LastSeen: g.LastSeen,
		Count: g.Count, Clients: g.Clients,
		ResolvedAt: g.ResolvedAt, ResolvedBy: g.ResolvedBy,
	}
}
