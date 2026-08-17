package http

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// AuditHandler — GET /api/audit (operator+, §26/§87).
type AuditHandler struct {
	uc     *usecase.AuditUsecase
	logger logging.Logger
}

func NewAuditHandler(uc *usecase.AuditUsecase, logger logging.Logger) *AuditHandler {
	return &AuditHandler{uc: uc, logger: logger}
}

type auditEntryResponse struct {
	ID         string         `json:"id"`
	UserID     string         `json:"user_id,omitempty"`
	UserLogin  string         `json:"user_login"`
	TeamID     string         `json:"team_id,omitempty"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type,omitempty"`
	TargetID   string         `json:"target_id,omitempty"`
	Details    map[string]any `json:"details"`
	IPAddress  string         `json:"ip_address,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func toAuditResp(e *domain.AuditEntry) auditEntryResponse {
	return auditEntryResponse{
		ID: e.ID, UserID: e.UserID, UserLogin: e.UserLogin, TeamID: e.TeamID,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Details: e.Details, IPAddress: e.IPAddress, CreatedAt: e.CreatedAt,
	}
}

// auditFilterFromQuery собирает фильтр из query-параметров. defaultLimit —
// значение по умолчанию для Limit (0 = без него). maxLimit — верхняя планка.
//
// TeamID по умолчанию — current_team_id из сессии (multi-tenancy v2,
// Phase 10.F.1). Чтобы посмотреть глобальный аудит, admin может передать
// ?team_id=* (или передать другой UUID — admin'у доверяем).
func auditFilterFromQuery(c *gin.Context, defaultLimit, maxLimit int) port.AuditFilter {
	teamScope := currentTeamID(c)
	if v := c.Query("team_id"); v != "" {
		if v == "*" {
			teamScope = ""
		} else {
			teamScope = v
		}
	}
	f := port.AuditFilter{
		UserID:     c.Query("user_id"),
		TeamID:     teamScope,
		TargetType: c.Query("target_type"),
		TargetID:   c.Query("target_id"),
		// §91.2: глобальные записи (входы, неудачные логины, восстановление
		// пароля, действия над пользователями) пишутся с team_id IS NULL и до
		// этого не попадали ни в один режим просмотра, кроме ручного team_id=*.
		// Админу они показываются вместе с журналом команды; operator/manager
		// продолжают видеть строго свою команду.
		IncludeGlobal: isAdminSession(c),
	}
	if v := c.Query("action"); v != "" && c.Query("actions") == "" {
		f.Actions = []string{v}
	}
	if v := c.Query("actions"); v != "" {
		f.Actions = splitCSV(v)
	}
	if v := c.Query("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := c.Query("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
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
	// §91.1: keyset-курсор работает только парой — половина курсора означала бы
	// молча другую выборку, а не «страницу дальше».
	if ts, id := c.Query("before_ts"), c.Query("before_id"); ts != "" && id != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			f.BeforeTS = &t
			f.BeforeID = id
		}
	}
	return f
}

// isAdminSession — роль текущей сессии admin? Для API-токенов роль берётся из
// псевдо-сессии, которую строит APITokenAuth, поэтому проверка едина.
func isAdminSession(c *gin.Context) bool {
	s, ok := sessionFromCtx(c)
	return ok && s.Role == domain.UserRoleAdmin
}

// listEntries — выбор скоупа журнала: команда сессии (или явный team_id) либо
// все команды пользователя при scope=all (§86.7). Общий путь для списка и
// CSV-выгрузки: два разбора одного скоупа разъехались бы, и выгрузка отдавала бы
// не то, что показано на экране.
//
// ok=false означает, что ответ уже записан.
func (h *AuditHandler) listEntries(c *gin.Context, f port.AuditFilter, op string) ([]*domain.AuditEntry, bool) {
	ctx := c.Request.Context()
	var (
		entries []*domain.AuditEntry
		err     error
	)
	if wantsAllTeams(c) {
		userID, allowed := resolveAllTeamsUser(c)
		if !allowed {
			return nil, false
		}
		entries, err = h.uc.ListAcrossTeams(ctx, userID, f)
	} else {
		entries, err = h.uc.List(ctx, f)
	}
	if err != nil {
		h.logger.ErrorWithOp("audit list failed", err, op)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return nil, false
	}
	return entries, true
}

// List godoc
// @Summary  Журнал audit-log (operator+, §7.13/§87).
// @Description  Поддерживаемые actions: node.create/update/delete, user.create/update/delete/password_changed, token.create/revoke/delete, login.success/failure, ch_table.drop, settings.update и др.
// @Tags     audit
// @Produce  json
// @Param    scope        query  string  false  "all — журнал всех команд пользователя (§86.7); НЕ то же, что team_id=* (весь инстанс, admin)"
// @Param    user_id      query  string  false  "фильтр по user_id"
// @Param    action       query  string  false  "одно значение action"
// @Param    actions      query  string  false  "несколько action через запятую"
// @Param    target_type  query  string  false  "node|user|token|ch_table|..."
// @Param    target_id    query  string  false  "фильтр по target_id"
// @Param    from         query  string  false  "RFC3339 (начало)"
// @Param    to           query  string  false  "RFC3339 (конец)"
// @Param    limit        query  int     false  "default 100, max 1000"
// @Param    offset       query  int     false  "смещение (legacy; для постраничного чтения используйте before_ts+before_id)"
// @Param    before_ts    query  string  false  "keyset-курсор §91.1: created_at последней показанной записи (RFC3339Nano). Работает только вместе с before_id"
// @Param    before_id    query  string  false  "keyset-курсор §91.1: id последней показанной записи. Работает только вместе с before_ts"
// @Success  200          {object}  ListAuditResponse
// @Failure  500          {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/audit [get]
func (h *AuditHandler) List(c *gin.Context) {
	f := auditFilterFromQuery(c, 100, 1000)

	entries, ok := h.listEntries(c, f, "audit.list")
	if !ok {
		return
	}
	out := make([]auditEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, toAuditResp(e))
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

// Count godoc
// @Summary  Сколько записей журнала подходит под фильтр (operator+, §91.1).
// @Description  Тот же набор фильтров, что у /api/audit, но без limit и курсора. Нужен счётчику «показано N из M»: список отдаёт страницу, и по нему нельзя понять, обрезана выдача или нет.
// @Tags     audit
// @Produce  json
// @Param    scope        query  string  false  "all — журнал всех команд пользователя (§86.7)"
// @Param    user_id      query  string  false  "фильтр по user_id"
// @Param    action       query  string  false  "одно значение action"
// @Param    actions      query  string  false  "несколько action через запятую"
// @Param    target_type  query  string  false  "node|user|token|ch_table|..."
// @Param    target_id    query  string  false  "фильтр по target_id"
// @Param    from         query  string  false  "RFC3339 (начало)"
// @Param    to           query  string  false  "RFC3339 (конец)"
// @Success  200          {object}  AuditCountResponse
// @Failure  500          {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/audit/count [get]
func (h *AuditHandler) Count(c *gin.Context) {
	// Лимиты не нужны: считаем всё подходящее под фильтр.
	f := auditFilterFromQuery(c, 0, 0)
	f.Limit, f.Offset = 0, 0

	ctx := c.Request.Context()
	var (
		n   int
		err error
	)
	if wantsAllTeams(c) {
		userID, allowed := resolveAllTeamsUser(c)
		if !allowed {
			return
		}
		n, err = h.uc.CountAcrossTeams(ctx, userID, f)
	} else {
		n, err = h.uc.Count(ctx, f)
	}
	if err != nil {
		h.logger.ErrorWithOp("audit count failed", err, "audit.count")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": n})
}

// ExportCSV godoc
// @Summary  Выгрузка audit-log в CSV (operator+, §7.13/§87).
// @Description  Тот же набор фильтров, что у /api/audit. Default limit 10000, max 50000. CSV в UTF-8 с BOM (для Excel), 9 колонок: id, created_at, user_login, user_id, action, target_type, target_id, ip_address, details (JSON).
// @Tags     audit
// @Produce  text/csv
// @Param    scope        query  string  false  "all — журнал всех команд пользователя (§86.7); НЕ то же, что team_id=* (весь инстанс, admin)"
// @Param    user_id      query  string  false  "фильтр по user_id"
// @Param    action       query  string  false  "одно значение action"
// @Param    actions      query  string  false  "несколько action через запятую"
// @Param    target_type  query  string  false  "node|user|token|ch_table|..."
// @Param    target_id    query  string  false  "фильтр по target_id"
// @Param    from         query  string  false  "RFC3339 (начало)"
// @Param    to           query  string  false  "RFC3339 (конец)"
// @Param    limit        query  int     false  "default 10000, max 50000"
// @Param    offset       query  int     false  "смещение"
// @Success  200          {string}  string  "CSV"
// @Failure  500          {object}  ErrorResponse
// @Security CookieAuth
// @Security ApiTokenAuth
// @Router   /api/audit/export.csv [get]
func (h *AuditHandler) ExportCSV(c *gin.Context) {
	f := auditFilterFromQuery(c, 10000, 50000)

	entries, ok := h.listEntries(c, f, "audit.export")
	if !ok {
		return
	}

	fname := fmt.Sprintf("audit-%s.csv", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fname))
	c.Header("Cache-Control", "no-store")

	w := csv.NewWriter(c.Writer)
	defer w.Flush()

	// BOM для корректного открытия в Excel (UTF-8).
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		h.logger.ErrorWithOp("audit export bom failed", err, "audit.export")
		return
	}
	header := []string{
		"id", "created_at", "user_login", "user_id", "team_id", "action",
		"target_type", "target_id", "ip_address", "details",
	}
	if err := w.Write(header); err != nil {
		h.logger.ErrorWithOp("audit export header failed", err, "audit.export")
		return
	}
	for _, e := range entries {
		details := ""
		if len(e.Details) > 0 {
			if b, mErr := json.Marshal(e.Details); mErr == nil {
				details = string(b)
			}
		}
		row := []string{
			e.ID,
			e.CreatedAt.Format(time.RFC3339Nano),
			e.UserLogin,
			e.UserID,
			e.TeamID,
			e.Action,
			e.TargetType,
			e.TargetID,
			e.IPAddress,
			details,
		}
		if err := w.Write(row); err != nil {
			h.logger.ErrorWithOp("audit export row failed", err, "audit.export")
			return
		}
	}
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
