package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Разбор параметра `scope` — сквозной режим «Все команды» (§86).
//
// Обычный режим (параметра нет) скоупится текущей командой сессии
// (`currentTeamID`, §18.3). `scope=all` подставляет вместо неё НАБОР команд, в
// которых пользователь состоит; сессионная current_team_id при этом не меняется —
// режим существует только на время запроса (§86.2).

// scopeAllValue — значение параметра `scope`, включающее сквозной режим.
//
// В URL адресной строки SPA тот же режим обозначается `?team=*` (§86.7.1) — там
// значение обязано не совпадать ни с одним slug'ом команды. Здесь отдельный
// параметр со словом `all`: он не конфликтует с именами команд, потому что
// сравнивается не со slug'ами, а с единственной константой.
const scopeAllValue = "all"

// wantsAllTeams — запрошен ли сквозной режим (§86).
//
// Регистр и пробелы нормализуются: параметр приходит из адресной строки, где его
// может набрать человек. Любое другое значение (включая пустое) — обычный режим
// по команде сессии, а не ошибка: неизвестный scope не должен ронять список.
func wantsAllTeams(c *gin.Context) bool {
	return strings.EqualFold(strings.TrimSpace(c.Query("scope")), scopeAllValue)
}

// resolveAllTeamsUser — пользователь, чьи членства задают сквозной скоуп.
//
// ok=false означает, что ответ уже записан и обработку нужно прекратить:
//
//   - API-токен → 403. Токен закреплён за одной командой (`token.team_id`,
//     §18.3), переключать её нельзя, и сквозная выдача сделала бы его
//     всекомандным в обход этого ограничения. Тот же довод, что у
//     `RequireSessionOnly` на /api/search/nodes (§62) и /me/switch-team (§18).
//   - нет сессии → 401 (защита от вызова вне authed-группы).
func resolveAllTeamsUser(c *gin.Context) (string, bool) {
	if _, isToken := c.Get(ctxAPITokenKey); isToken {
		c.JSON(http.StatusForbidden, ErrorResponse{Error: "scope=all is not available via API token"})
		return "", false
	}
	s, ok := sessionFromCtx(c)
	if !ok || s.UserID == "" {
		c.JSON(http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
		return "", false
	}
	return s.UserID, true
}
