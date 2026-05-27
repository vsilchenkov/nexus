// Package http — HTTP-handlers Receiver Service (Gin).
package http

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/receiver/usecase"
)

// Handler — /v1/request/* и /v1/requestAsync/*.
type Handler struct {
	route        *usecase.RouteUsecase
	routeAsync   *usecase.RouteAsyncUsecase
	logger       logging.Logger
	maxBodyBytes int
}

func New(
	route *usecase.RouteUsecase,
	routeAsync *usecase.RouteAsyncUsecase,
	maxBodyBytes int,
	logger logging.Logger,
) *Handler {
	return &Handler{route: route, routeAsync: routeAsync, logger: logger, maxBodyBytes: maxBodyBytes}
}

// Register вешает /v1/request/*path и /v1/requestAsync/*path на роутер.
//
// Phase 10.E.1: маршруты включают team_slug. Полный путь —
// /v1/request/<team_slug>/<node_path>. Legacy без слога
// (/v1/request/<node_path>) сохраняется как convenience для default-team:
// запросы без префикса слога продолжают работать, NodeReader подставляет
// domain.DefaultTeamSlug.
//
// Префикс /v1/ обязателен; запрос без него — 404 с подсказкой (§3.1).
// mws — дополнительные middleware (rate-limit, audit, ...), применяются
// перед основным handler'ом.
func (h *Handler) Register(r *gin.Engine, mws ...gin.HandlerFunc) {
	// Корневой 404 для запросов без /v1/.
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/request") || strings.HasPrefix(p, "/requestAsync") || strings.HasPrefix(p, "/callback") {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "API version required, use /v1/...",
			})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})

	v1 := r.Group("/v1", mws...)
	{
		v1.Any("/request/*path", h.handleSync)
		v1.Any("/requestAsync/*path", h.handleAsync)
		// §16 ТЗ: webhook callback. Alias /v1/requestAsync с обязательной
		// проверкой того, что у узла IncomingAuthType=webhook_signature.
		// Сама проверка подписи происходит в RouteAsync через
		// CheckIncomingAuth (общий путь, без дублирования логики).
		v1.POST("/callback/*path", h.handleCallback)
	}
}

// splitTeamSlugAndPath режет catch-all сегмент Gin (`/foo/bar/baz`) на
// (team_slug, node_path). Первый сегмент — slug команды (мульти-tenancy
// v2). Один сегмент = legacy URL без слога: возвращает teamSlug=""
// (NodeReader подставит DefaultTeamSlug).
//
// Также допускается единственный «не-slug» сегмент, в котором есть
// разрешённые в node_path символы '/' (после catch-all gin всегда даёт
// строку с ведущим '/').
func splitTeamSlugAndPath(raw string) (teamSlug, nodePath string) {
	trimmed := strings.TrimPrefix(raw, "/")
	if trimmed == "" {
		return "", ""
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 1 {
		// Один сегмент — legacy URL, считаем что это node_path в default-team.
		return "", parts[0]
	}
	return parts[0], parts[1]
}

func (h *Handler) handleSync(c *gin.Context) {
	teamSlug, nodePath := splitTeamSlugAndPath(c.Param("path"))
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}

	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	in := usecase.RouteInput{
		TeamSlug: teamSlug,
		NodePath: nodePath,
		Method:   c.Request.Method,
		Header:   c.Request.Header,
		Query:    c.Request.URL.Query(),
		Body:     body,
		ClientIP: clientIP(c.Request),
	}
	out, err := h.route.Route(c.Request.Context(), in)
	if err != nil {
		// §3.6: paused-узел в sync-режиме переключается на async и
		// отвечает 202 + queued:true (см. handleAsync).
		if errors.Is(err, domain.ErrNodePaused) {
			h.handleAsyncFromInput(c, in)
			return
		}
		h.replyDomainError(c, err, nodePath, "receiver.sync")
		return
	}
	for k, v := range out.Headers {
		// Пропускаем hop-by-hop и небезопасные.
		if isHopByHopHeader(k) {
			continue
		}
		c.Header(k, v)
	}
	c.Data(out.StatusCode, out.Headers["Content-Type"], out.Body)
}

// handleCallback обрабатывает POST /v1/callback/{path} — приём входящего
// webhook'а от внешнего провайдера (§16 ТЗ). Алиас /v1/requestAsync с
// проверкой что узел сконфигурирован под webhook_signature: иначе вернём
// 400, чтобы случайные клиенты не дёргали callback-endpoint с обычными
// узлами в обход separation of concerns.
//
// Сама HMAC-проверка делается централизованно в CheckIncomingAuth внутри
// RouteAsync — handler здесь не выполняет crypto-логику.
func (h *Handler) handleCallback(c *gin.Context) {
	teamSlug, nodePath := splitTeamSlugAndPath(c.Param("path"))
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.handleAsyncFromInput(c, usecase.RouteInput{
		TeamSlug:        teamSlug,
		NodePath:        nodePath,
		Method:          c.Request.Method,
		Header:          c.Request.Header,
		Query:           c.Request.URL.Query(),
		Body:            body,
		ClientIP:        clientIP(c.Request),
		RequireCallback: true,
	})
}

func (h *Handler) handleAsync(c *gin.Context) {
	teamSlug, nodePath := splitTeamSlugAndPath(c.Param("path"))
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.handleAsyncFromInput(c, usecase.RouteInput{
		TeamSlug: teamSlug,
		NodePath: nodePath,
		Method:   c.Request.Method,
		Header:   c.Request.Header,
		Query:    c.Request.URL.Query(),
		Body:     body,
		ClientIP: clientIP(c.Request),
	})
}

// handleAsyncFromInput выполняет async-маршрутизацию по уже подготовленному
// RouteInput. Вызывается как из /v1/requestAsync, так и из sync-handler'а,
// когда узел в paused (§3.6).
func (h *Handler) handleAsyncFromInput(c *gin.Context, in usecase.RouteInput) {
	res, err := h.routeAsync.RouteAsync(c.Request.Context(), in)
	if err != nil {
		h.replyDomainError(c, err, in.NodePath, "receiver.async")
		return
	}

	// §3.6 ТЗ: paused-узел отвечает 202 + queued:true + node_status:paused.
	if res.Queued {
		c.JSON(http.StatusAccepted, gin.H{
			"result":      true,
			"id":          res.ID,
			"queued":      true,
			"node_status": string(res.NodeStatus),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": true, "id": res.ID})
}

func readBody(c *gin.Context, max int) ([]byte, error) {
	if max <= 0 {
		max = 5 * 1024 * 1024
	}
	r := io.LimitReader(c.Request.Body, int64(max+1))
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(body) > max {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

func clientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.Index(xf, ","); i >= 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	if r.RemoteAddr != "" {
		if i := strings.LastIndex(r.RemoteAddr, ":"); i >= 0 {
			return r.RemoteAddr[:i]
		}
		return r.RemoteAddr
	}
	return ""
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

func (h *Handler) replyDomainError(c *gin.Context, err error, nodePath, op string) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
	case errors.Is(err, domain.ErrNodeDisabled):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "node not available"})
	case errors.Is(err, domain.ErrURLParamRequired),
		errors.Is(err, domain.ErrCallbackNotAllowed):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, domain.ErrURLInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "target url is invalid"})
	case errors.Is(err, domain.ErrURLNotAllowed):
		c.JSON(http.StatusForbidden, gin.H{"error": "target url not in allowlist"})
	case errors.Is(err, domain.ErrAuthHeaderMissing),
		errors.Is(err, domain.ErrAuthHeaderMalformed),
		errors.Is(err, domain.ErrAuthTokenRequired),
		errors.Is(err, domain.ErrUnauthorized):
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
	default:
		h.logger.ErrorWithOp("receiver routing failed", err, op,
			h.logger.Str("node", nodePath))
		c.JSON(http.StatusBadGateway, gin.H{"error": "internal routing error"})
	}
}
