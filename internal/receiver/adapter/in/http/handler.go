// Package http — HTTP-handlers Receiver Service (Gin).
package http

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/receiver/usecase"
)

// Handler — /v1/request/* и /v1/requestAsync/*.
type Handler struct {
	route        *usecase.RouteUsecase
	routeAsync   *usecase.RouteAsyncUsecase
	metrics      *metrics.Metrics
	logger       logging.Logger
	maxBodyBytes int
}

func New(
	route *usecase.RouteUsecase,
	routeAsync *usecase.RouteAsyncUsecase,
	maxBodyBytes int,
	m *metrics.Metrics,
	logger logging.Logger,
) *Handler {
	return &Handler{route: route, routeAsync: routeAsync, metrics: m, logger: logger, maxBodyBytes: maxBodyBytes}
}

// Register вешает боевые маршруты шины на роутер — ОДНИМ catch-all
// /api/v1/*path, внутри которого первый сегмент разбирает handleIngress.
//
// Phase 10.E.1: маршруты включают team_slug. Полный путь —
// /api/v1/request/<team_slug>/<node_path>. Legacy без слога
// (/api/v1/request/<node_path>) сохраняется как convenience для default-team:
// запросы без префикса слога продолжают работать, NodeReader подставляет
// domain.DefaultTeamSlug.
//
// §78.2: адрес узла может идти и БЕЗ сегмента метода
// (/api/v1/<team_slug>/<node_path>) — тогда синхронность берётся из
// node.root_method. Отдельным маршрутом это не сделать: gin роняет роутер при
// старте, если catch-all соседствует с уже занятым сегментом
// («catch-all wildcard '*path' … conflicts with existing path segment
// 'request'»). Разбор в NoRoute тоже не годится — туда не доходят middleware
// группы: rate-limit не применился бы вовсе, а metrics.GinMiddleware выходит
// досрочно при пустом c.FullPath(), и боевой трафик исчез бы из метрик.
//
// Префикс /api/v1/ обязателен; запрос без него — 404 с подсказкой (§3.1).
// mws — дополнительные middleware (rate-limit, audit, ...), применяются
// перед основным handler'ом.
func (h *Handler) Register(r *gin.Engine, mws ...gin.HandlerFunc) {
	// Корневой 404 для запросов без /api/v1/.
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/request") || strings.HasPrefix(p, "/requestAsync") ||
			strings.HasPrefix(p, "/callback") || strings.HasPrefix(p, "/v1/") {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "API version required, use /api/v1/...",
			})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	})

	v1 := r.Group("/api/v1", mws...)
	v1.Any("/*path", h.handleIngress)
}

// Сегменты-методы в начале пути (legacy-форма адреса узла). Регистр значим —
// ровно так они писались в маршрутах до §78.
const (
	verbRequest      = "request"
	verbRequestAsync = "requestAsync"
	verbCallback     = "callback"
)

// SplitVerb отделяет ведущий сегмент-метод от остатка пути. Остаток всегда
// начинается с '/' — в том же виде, в каком его отдавал catch-all каждого из
// трёх прежних маршрутов, поэтому дальше по коду ничего не меняется.
//
// Путь без известного сегмента-метода — короткая форма §78.1: verb пуст, весь
// путь идёт в остаток.
//
// Экспортирована ради RateLimitMiddleware: ключ лимита обязан считаться от
// одного и того же остатка для обеих форм адреса, иначе клиент удваивал бы
// квоту простой сменой формы (и все ключи Redis сменились бы при выкате).
func SplitVerb(raw string) (verb, rest string) {
	trimmed := strings.TrimPrefix(raw, "/")
	head, tail, _ := strings.Cut(trimmed, "/")
	switch head {
	case verbRequest, verbRequestAsync, verbCallback:
		return head, "/" + tail
	}
	return "", raw
}

// handleIngress — единая точка входа боевого трафика: разбирает ведущий
// сегмент-метод и ведёт запрос в ту же ветку, что и до §78.
func (h *Handler) handleIngress(c *gin.Context) {
	verb, rest := SplitVerb(c.Param("path"))
	switch verb {
	case verbRequest:
		h.handleSync(c, rest)
	case verbRequestAsync:
		h.handleAsync(c, rest)
	case verbCallback:
		// До §78 маршрут был POST-only, и другой метод падал в NoRoute (404).
		// Теперь сюда доходит любой — отвечаем честным 405.
		if c.Request.Method != http.MethodPost {
			c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "callback accepts POST only"})
			return
		}
		h.handleCallback(c, rest)
	default:
		h.handleAuto(c, rest)
	}
}

// handleAuto — короткая форма адреса §78.1: /api/v1/<team_slug>/<node_path> без
// сегмента метода. Синхронность — свойство узла, поэтому она резолвится из
// конфигурации, а дальше запрос идёт по тому же коду, что и legacy-URL.
func (h *Handler) handleAuto(c *gin.Context, rest string) {
	teamSlug, nodePath := splitTeamSlugAndPath(rest)
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	// Метка node — как в handleSync/handleAsync: путь узла без слога команды.
	// Ставится до резолва, чтобы 404 по короткому адресу метился так же, как
	// 404 по legacy-адресу того же пути.
	c.Set(metrics.NodeLabelKey, nodePath)
	root, err := h.route.NodeRootMethod(c.Request.Context(), teamSlug, nodePath)
	if err != nil {
		c.Set(metrics.RootMethodLabelKey, metrics.RootMethodShortURL)
		h.replyDomainError(c, err, nodePath, "receiver.auto")
		return
	}
	switch root {
	case domain.RootMethodRequest:
		h.handleSync(c, rest)
	case domain.RootMethodRequestAsync:
		h.handleAsync(c, rest)
	default:
		// Pull-узел (RabbitMQAsync §27) входящего HTTP не имеет. Отвечаем как на
		// несуществующий узел: факт существования наружу не раскрываем.
		c.Set(metrics.RootMethodLabelKey, metrics.RootMethodShortURL)
		h.logger.Debug("receiver.auto: node has no http ingress",
			h.logger.Str("path", nodePath),
			h.logger.Str("root_method", string(root)))
		h.replyDomainError(c, domain.ErrNodeNotFound, nodePath, "receiver.auto")
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

// handleSync godoc
// @Summary  Синхронный запрос через узел (§3.1).
// @Description  Проксирует входящий запрос на внешний адрес узла и возвращает его ответ. Путь — /api/v1/request/<team_slug>/<node_path> (slug опционален для default-команды). Метод, тело и заголовки зависят от конфигурации узла.
// @Tags     routing
// @Param    path  path  string  true  "[<team_slug>/]<node_path>"
// @Success  200  {object}  map[string]interface{}  "ответ внешнего узла (тело/код проксируются)"
// @Failure  403  {object}  map[string]string  "url not in allowlist"
// @Failure  404  {object}  map[string]string  "node not found"
// @Router   /api/v1/request/{path} [post]
//
// rest — путь после сегмента метода (или весь путь при короткой форме §78.1),
// всегда с ведущим '/'.
func (h *Handler) handleSync(c *gin.Context, rest string) {
	teamSlug, nodePath := splitTeamSlugAndPath(rest)
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	// Метка node для метрик — чистый путь узла (без слога команды), чтобы
	// совпадать с меткой Sender и корректно мёрджить in/out на дашборде (§21).
	c.Set(metrics.NodeLabelKey, nodePath)
	// §78.2: метка method больше не выводится из c.FullPath() — он один на все
	// формы адреса. Значение прежнее, поэтому ряды Prometheus не разъезжаются и
	// трафик по короткому адресу виден в существующих панелях.
	c.Set(metrics.RootMethodLabelKey, string(domain.RootMethodRequest))

	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		replyReadBodyError(c, err) // §43-rev: превышение max_body_bytes → 413
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
// handleCallback godoc
// @Summary  Webhook-callback (§16).
// @Description  Приём входящего webhook'а от внешнего провайдера. Alias асинхронного маршрута с обязательной проверкой HMAC-подписи (узел должен быть incoming_auth_type=webhook_signature).
// @Tags     routing
// @Param    path  path  string  true  "[<team_slug>/]<node_path>"
// @Success  200  {object}  map[string]interface{}
// @Failure  400  {object}  map[string]string  "callback not allowed for this node"
// @Router   /api/v1/callback/{path} [post]
//
// rest — путь после сегмента callback, с ведущим '/'.
func (h *Handler) handleCallback(c *gin.Context, rest string) {
	teamSlug, nodePath := splitTeamSlugAndPath(rest)
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	// §78.2: собственная метка method вместо прежнего fallback'а по имени
	// маршрута — c.FullPath() теперь один на все формы адреса.
	c.Set(metrics.RootMethodLabelKey, metrics.RootMethodCallback)
	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		replyReadBodyError(c, err) // §43-rev: превышение max_body_bytes → 413
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

// handleAsync godoc
// @Summary  Асинхронный запрос через узел (§3.1).
// @Description  Ставит запрос в очередь Kafka и сразу отвечает {result:true,id}. Доставку выполняет Sender-consumer. Путь — /api/v1/requestAsync/<team_slug>/<node_path>.
// @Tags     routing
// @Param    path  path  string  true  "[<team_slug>/]<node_path>"
// @Success  200  {object}  map[string]interface{}  "{result:true,id}"
// @Success  202  {object}  map[string]interface{}  "queued (paused node, §3.6)"
// @Failure  404  {object}  map[string]interface{}  "{result:false,message} — node not found"
// @Failure  405  {object}  map[string]interface{}  "{result:false,message} — method not allowed"
// @Router   /api/v1/requestAsync/{path} [post]
//
// rest — путь после сегмента метода (или весь путь при короткой форме §78.1),
// всегда с ведущим '/'.
func (h *Handler) handleAsync(c *gin.Context, rest string) {
	teamSlug, nodePath := splitTeamSlugAndPath(rest)
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	// §78.2: см. handleSync. Метку ставит вызывающий, а не handleAsyncFromInput:
	// sync-узел на паузе уходит в async-ветку (§3.6), но остаётся "request" —
	// ровно как метился по имени маршрута до §78.
	c.Set(metrics.RootMethodLabelKey, string(domain.RootMethodRequestAsync))
	body, err := readBody(c, h.maxBodyBytes)
	if err != nil {
		replyReadBodyError(c, err) // §43-rev: превышение max_body_bytes → 413
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
	// Метка node для метрик — чистый путь узла (см. handleSync).
	c.Set(metrics.NodeLabelKey, in.NodePath)
	res, err := h.routeAsync.RouteAsync(c.Request.Context(), in)
	if err != nil {
		// §3, #7: async-ошибка → {"result":false,"message":...}.
		h.replyAsyncError(c, err, in.NodePath, "receiver.async")
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

// errBodyTooLarge — тело запроса превысило receiver.max_body_bytes (config).
// Обрабатывается как 413 (а не 400) в хендлерах — это транспортный лимит шины,
// а не «битый запрос» (§43-rev).
var errBodyTooLarge = errors.New("request body too large")

// drainCap — потолок «дренажа» остатка тела при превышении лимита: дочитываем
// и выбрасываем (io.Discard, без памяти) до этого объёма, чтобы запрос завершился
// штатно и ответ 413 дошёл до клиента/прокси, а не превратился в TCP-reset → 502.
// Очень большие тела (> лимит + drainCap) всё равно оборвут соединение — это
// защита от slow/DoS-дренажа.
const drainCap = 8 << 20 // 8 МиБ

func readBody(c *gin.Context, max int) ([]byte, error) {
	if max <= 0 {
		max = 5 * 1024 * 1024
	}
	// Ранний отказ по заявленному Content-Length — ДО чтения тела. Для клиентов
	// с Expect: 100-continue (curl добавляет его на тела > 1 МБ) это даёт чистый
	// 413 без reset'а: получив 413 на Expect, клиент тело вообще не отправляет,
	// и ответ цельным доходит через Web-прокси (а не превращается в 502).
	if c.Request.ContentLength > int64(max) {
		return nil, errBodyTooLarge
	}
	r := io.LimitReader(c.Request.Body, int64(max+1))
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(body) > max {
		// chunked / без Content-Length: дренируем ограниченный остаток, чтобы
		// 413 дошёл цельным ответом (иначе сервер reset'ит соединение с
		// непрочитанным телом → прокси 502).
		_, _ = io.CopyN(io.Discard, c.Request.Body, drainCap)
		return nil, errBodyTooLarge
	}
	return body, nil
}

// replyReadBodyError мапит ошибку readBody в HTTP-ответ: превышение лимита →
// 413 Payload Too Large, прочее (обрыв чтения) → 400.
func replyReadBodyError(c *gin.Context, err error) {
	if errors.Is(err, errBodyTooLarge) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

// clientIP извлекает IP клиента (X-Forwarded-For → RemoteAddr) и нормализует
// его к IPv4, где возможно (§4 ТЗ: в логах фиксируем ip4, а не ip6).
func clientIP(r *http.Request) string {
	return clientip.NormalizeIPv4(rawClientIP(r))
}

func rawClientIP(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if before, _, ok := strings.Cut(xf, ","); ok {
			return strings.TrimSpace(before)
		}
		return strings.TrimSpace(xf)
	}
	if r.RemoteAddr != "" {
		// RemoteAddr — "host:port"; host может быть IPv6 в скобках
		// ("[::1]:1234"). net.SplitHostPort корректно их разбирает.
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return host
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

// classifyDomainError маппит доменную ошибку маршрутизации в HTTP-код и
// человекочитаемое сообщение. internal=true означает «непредвиденная ошибка»
// (502) — её caller дополнительно логирует. Общая для sync (replyDomainError)
// и async (replyAsyncError), чтобы коды и тексты не расходились.
func classifyDomainError(err error) (status int, message string, internal bool) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound):
		return http.StatusNotFound, "node not found", false
	case errors.Is(err, domain.ErrNodeDisabled):
		return http.StatusServiceUnavailable, "node not available", false
	case errors.Is(err, domain.ErrNodeMethodNotAllowed):
		return http.StatusMethodNotAllowed, "http method not allowed for this node", false
	case errors.Is(err, domain.ErrURLParamRequired),
		errors.Is(err, domain.ErrCallbackNotAllowed):
		return http.StatusBadRequest, err.Error(), false
	case errors.Is(err, domain.ErrURLInvalid):
		return http.StatusBadRequest, "target url is invalid", false
	case errors.Is(err, domain.ErrURLNotAllowed):
		return http.StatusForbidden, "target url not in allowlist", false
	case errors.Is(err, domain.ErrLoopDetected):
		// §32: запрос вернулся в шину больше max_hops раз — петля.
		return http.StatusLoopDetected, "loop detected", false
	case errors.Is(err, domain.ErrAuthHeaderMissing),
		errors.Is(err, domain.ErrAuthHeaderMalformed),
		errors.Is(err, domain.ErrAuthTokenRequired),
		errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized, err.Error(), false
	default:
		return http.StatusBadGateway, "internal routing error", true
	}
}

// replyDomainError — sync-ответ об ошибке: {"error": <msg>}.
func (h *Handler) replyDomainError(c *gin.Context, err error, nodePath, op string) {
	status, msg, internal := classifyDomainError(err)
	if internal {
		h.logger.ErrorWithOp("receiver routing failed", err, op,
			h.logger.Str("node", nodePath))
	} else {
		// §51.9: 4xx раньше уходили молча — «почему клиенту 404/401?» было
		// невосстановимо. Debug, чтобы не шуметь на проде на каждый скан.
		h.logger.Debug("receiver request rejected",
			h.logger.Int("status", status),
			h.logger.Str("node", nodePath),
			h.logger.Str("op", op),
			h.logger.Str("method", c.Request.Method),
			h.logger.Str("client_ip", c.ClientIP()),
			h.logger.Err(err))
	}
	if errors.Is(err, domain.ErrLoopDetected) {
		h.onLoopDetected(c, "sync", nodePath)
	}
	c.JSON(status, gin.H{"error": msg})
}

// onLoopDetected фиксирует обнаружение петли (§32): warn-лог + Prometheus-счётчик
// nexus_loop_detected_total{mode}. metrics может быть nil в unit-тестах handler'а.
func (h *Handler) onLoopDetected(c *gin.Context, mode, nodePath string) {
	h.logger.Warn("loop detected: request exceeded max hops",
		h.logger.Str("op", "receiver.loop"),
		h.logger.Str("node", nodePath),
		h.logger.Str("mode", mode),
		h.logger.Str("hops", c.Request.Header.Get(usecase.HeaderHops)))
	if h.metrics != nil {
		h.metrics.IncLoopDetected(mode)
	}
}

// replyAsyncError — async-ответ об ошибке (§3, #7): {"result": false,
// "message": <причина>}. Тело отличается от sync-варианта, чтобы async-клиент
// единообразно читал result/message и в успехе, и в ошибке.
func (h *Handler) replyAsyncError(c *gin.Context, err error, nodePath, op string) {
	status, msg, internal := classifyDomainError(err)
	if internal {
		h.logger.ErrorWithOp("receiver async routing failed", err, op,
			h.logger.Str("node", nodePath))
	} else {
		// §51.9: см. replyDomainError — отказы клиенту видны на debug.
		h.logger.Debug("receiver async request rejected",
			h.logger.Int("status", status),
			h.logger.Str("node", nodePath),
			h.logger.Str("op", op),
			h.logger.Str("method", c.Request.Method),
			h.logger.Str("client_ip", c.ClientIP()),
			h.logger.Err(err))
	}
	if errors.Is(err, domain.ErrLoopDetected) {
		h.onLoopDetected(c, "async", nodePath)
	}
	c.JSON(status, gin.H{"result": false, "message": msg})
}
