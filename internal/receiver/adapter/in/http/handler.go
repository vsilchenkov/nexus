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
	"nexus/internal/platform/clientgone"
	"nexus/internal/platform/clientip"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
	"nexus/internal/platform/sensitive"
	"nexus/internal/receiver/usecase"
)

// Handler — /v1/request/* и /v1/requestAsync/*.
type Handler struct {
	route      *usecase.RouteUsecase
	routeAsync *usecase.RouteAsyncUsecase
	metrics    *metrics.Metrics
	logger     logging.Logger
	// limits — действующие лимиты размера тела (§97). Не два числа, а
	// провайдер: администратор меняет их в интерфейсе, и новое значение
	// применяется без рестарта (секция general, Redis pub/sub). Пара
	// (sync, async) читается атомарно — см. BodyLimitsProvider.Limits.
	limits *usecase.BodyLimitsProvider
}

// New создаёт handler боевого трафика. limits — провайдер действующих лимитов
// размера тела (§97); nil допустим и означает «дефолт домена на обоих путях».
//
// Нормализация лимитов («0 = дефолт», «async ≤ sync», зажатие потолком из
// конфига) живёт в провайдере, а не здесь: async-потолок сравнивается с длиной
// уже прочитанного тела (§3.6), и «0» на этой ветке означало бы «отвергать всё
// непустое».
func New(
	route *usecase.RouteUsecase,
	routeAsync *usecase.RouteAsyncUsecase,
	limits *usecase.BodyLimitsProvider,
	m *metrics.Metrics,
	logger logging.Logger,
) *Handler {
	return &Handler{
		route: route, routeAsync: routeAsync, metrics: m, logger: logger,
		limits: limits,
	}
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

// handleAuto godoc
// @Summary  Запрос через узел без указания метода (§78.1).
// @Description  Короткая форма адреса: /api/v1/<team_slug>/<node_path> (для команды default слог можно опустить). Синхронный это узел или асинхронный, определяет его root_method, поэтому в адресе метод не указывается. Формы /api/v1/request/... и /api/v1/requestAsync/... продолжают работать.
// @Tags     routing
// @Param    path  path  string  true  "[<team_slug>/]<node_path>"
// @Success  200  {object}  map[string]interface{}  "ответ внешнего узла (sync) либо {result:true,id} (async)"
// @Success  202  {object}  map[string]interface{}  "queued (paused node, §3.6)"
// @Failure  404  {object}  map[string]string  "node not found (в т.ч. узел RabbitMQAsync — входящего HTTP у него нет)"
// @Failure  405  {object}  map[string]string  "метод не принимается узлом; либо GET-подписка SSE у узла с отказом от потока (§107, заголовок Allow)"
// @Failure  503  {object}  map[string]string  "sender unavailable — Sender перезапускается и не поднялся за sender_grpc.ready_wait_ms; вызов не отправлялся, повторить после Retry-After (§109.3)"
// @Router   /api/v1/{path} [post]
//
// handleAuto — короткая форма адреса §78.1: /api/v1/<team_slug>/<node_path> без
// сегмента метода. Синхронность — свойство узла, поэтому она резолвится из
// конфигурации, а дальше запрос идёт по тому же коду, что и legacy-URL.
func (h *Handler) handleAuto(c *gin.Context, rest string) {
	teamSlug, nodePath := splitTeamSlugAndPath(rest)
	if nodePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty node path"})
		return
	}
	// §101.3: метки узла ставит наблюдатель резолва, а не адрес; 404 метится
	// <unresolved> в markRejected — так же, как по legacy-адресу того же пути.
	root, err := h.route.NodeRootMethod(c.Request.Context(), teamSlug, nodePath, nodeLabelSetter(c))
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
			h.logger.Str("path", sensitive.MaskBotTokens(nodePath)),
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
// @Failure  405  {object}  map[string]string  "метод не принимается узлом; либо GET-подписка SSE у узла с отказом от потока (§107, заголовок Allow)"
// @Failure  503  {object}  map[string]string  "sender unavailable — Sender перезапускается и не поднялся за sender_grpc.ready_wait_ms; вызов не отправлялся, повторить после Retry-After (§109.3)"
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
	// §78.2: метка method больше не выводится из c.FullPath() — он один на все
	// формы адреса. Значение прежнее, поэтому ряды Prometheus не разъезжаются и
	// трафик по короткому адресу виден в существующих панелях.
	c.Set(metrics.RootMethodLabelKey, string(domain.RootMethodRequest))

	// Пара лимитов читается ОДНИМ снимком: ниже, на ветке paused-узла (§3.6),
	// нужен async-лимит, и он обязан быть из той же версии настроек, что и
	// sync-лимит, по которому тело уже прочитано.
	syncLimit, asyncLimit := h.limits.Limits()

	body, err := readBody(c, syncLimit)
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
		// §101.3: метки узла — только из резолва, см. nodeLabelSetter.
		OnNodeResolved: nodeLabelSetter(c),
	}
	out, err := h.route.Route(c.Request.Context(), in)
	if err != nil {
		h.replySyncRouteError(c, in, err, nodePath, asyncLimit)
		return
	}
	for k, v := range out.Headers {
		// Пропускаем hop-by-hop и небезопасные.
		if isHopByHopHeader(k) {
			continue
		}
		c.Header(k, v)
	}
	// §94: статус ниже — ответ ВНЕШНЕЙ системы, а не решение шины. Без этой
	// пометки штатный 400 от апстрима (Telegram «message is not modified»)
	// попадал бы в журнал отказов как отказ шины.
	MarkProxiedResponse(c)
	c.Data(out.StatusCode, out.Headers["Content-Type"], out.Body)
}

// replySyncRouteError — ответ на ошибку sync-маршрутизации: paused-узел уходит
// в очередь (§3.6), отказ от SSE-подписки отвечает 405 по настройке узла
// (§107.2), остальное — доменная ошибка.
func (h *Handler) replySyncRouteError(c *gin.Context, in usecase.RouteInput, err error, nodePath string, asyncLimit int) {
	var declined *domain.StreamDeclinedError
	switch {
	case errors.Is(err, domain.ErrNodePaused):
		// §3.6 + разделённые лимиты: sync принимает тела крупнее, чем влезает
		// в Kafka-конверт. Запрос к paused-узлу уходит в очередь, поэтому
		// здесь действует АСИНХРОННЫЙ потолок — иначе тело было бы принято
		// (200 клиенту), а затем отвергнуто брокером при публикации, то есть
		// потеряно молча.
		if len(in.Body) > asyncLimit {
			h.logger.Warn("sync request to paused node exceeds async body limit",
				h.logger.Str("node", nodeForLog(c, nodePath)),
				h.logger.Int("body_len", len(in.Body)),
				h.logger.Int("limit", asyncLimit))
			replyReadBodyError(c, errBodyTooLargeForQueue)
			return
		}
		h.handleAsyncFromInput(c, in)
	case errors.As(err, &declined):
		h.replyStreamDeclined(c, declined, nodePath)
	default:
		h.replyDomainError(c, err, nodePath, "receiver.sync")
	}
}

// replyStreamDeclined — §107.2: узел не открывает поток событий на GET. 405
// задала настройка узла, а не отказ шины, поэтому ответ помечен как
// MarkProxiedResponse — тот же механизм, что у статусов получателя и шаблона
// приёма §83: журнал отказов §94 и nexus_ingress_rejected_total его не видят,
// а nexus_requests_total{status="405"} видит.
//
// Allow обязателен в 405 (RFC 9110 §15.5.6) и ставится даже пустым — узел,
// принимающий только GET, другого метода не предлагает. c.Header с пустым
// значением заголовок удалил бы, поэтому запись идёт напрямую.
func (h *Handler) replyStreamDeclined(c *gin.Context, declined *domain.StreamDeclinedError, nodePath string) {
	h.logger.Debug("receiver.sync: sse subscription declined by node setting",
		h.logger.Str("node", nodeForLog(c, nodePath)),
		h.logger.Str("team", c.GetString(metrics.TeamLabelKey)),
		h.logger.Str("method", c.Request.Method),
		h.logger.Str("allow", declined.Allow))
	c.Writer.Header().Set("Allow", declined.Allow)
	MarkProxiedResponse(c)
	c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "event stream is not supported by this node"})
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
	body, err := readBody(c, h.limits.Async())
	if err != nil {
		replyReadBodyError(c, err) // §43-rev: превышение max_async_body_bytes → 413
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
// @Description  Ставит запрос в очередь Kafka и сразу отвечает {result:true,id}. Доставку выполняет Sender-consumer. Путь — /api/v1/requestAsync/<team_slug>/<node_path>. §82.3: узел обязан быть настроен как requestAsync — иначе 404 (как и несуществующий узел).
// @Tags     routing
// @Param    path  path  string  true  "[<team_slug>/]<node_path>"
// @Success  200  {object}  map[string]interface{}  "{result:true,id}"
// @Success  202  {object}  map[string]interface{}  "queued (paused node, §3.6)"
// @Failure  404  {object}  map[string]interface{}  "{result:false,message} — node not found (в т.ч. узел с root_method != requestAsync, §82.3)"
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
	body, err := readBody(c, h.limits.Async())
	if err != nil {
		replyReadBodyError(c, err) // §43-rev: превышение max_async_body_bytes → 413
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
		// §82.3: единственная точка, где флаг ставится — внешний async-эндпоинт.
		// Короткая форма §78.1 сюда приходит только для узлов requestAsync
		// (handleAuto разводит по root_method), так что проверка ей безразлична.
		ExternalAsync: true,
	})
}

// handleAsyncFromInput выполняет async-маршрутизацию по уже подготовленному
// RouteInput. Вызывается как из /v1/requestAsync, так и из sync-handler'а,
// когда узел в paused (§3.6).
func (h *Handler) handleAsyncFromInput(c *gin.Context, in usecase.RouteInput) {
	// §101.3: метки узла ставит наблюдатель резолва. У paused-sync (§3.6) он уже
	// сработал в Route и сработает второй раз с теми же значениями.
	in.OnNodeResolved = nodeLabelSetter(c)
	res, err := h.routeAsync.RouteAsync(c.Request.Context(), in)
	if err != nil {
		// §3, #7: async-ошибка → {"result":false,"message":...}.
		h.replyAsyncError(c, err, in.NodePath, "receiver.async")
		return
	}

	// §83: шаблон не отработал, но узел настроен «отвечать как раньше». Warn
	// уже написан в usecase; метрика — единственный внешний признак, по которому
	// видно, что интеграция получает НЕ тот ответ, которого ждёт.
	if res.AckDegraded != "" && h.metrics != nil {
		h.metrics.IncAckRenderFailed(metrics.NodeLabelsFromGin(c), res.AckDegraded)
	}

	// §83: собственный ответ узла — вместо обеих штатных форм ниже.
	if res.Ack != nil {
		// nosniff: тело собирает оператор, и браузер не должен додумывать тип
		// за объявленным. X-Nexus-Id сохраняет связь ответа с записью журнала —
		// в кастомном теле идентификатора шины может не быть вовсе.
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header(domain.HeaderNexusID, res.ID)
		// §83: статус задаёт шаблон ответа приёма, настроенный на узле, — это
		// тоже не отказ шины (сам приём состоялся, сообщение в очереди).
		MarkProxiedResponse(c)
		c.Data(res.Ack.Status, res.Ack.ContentType, res.Ack.Body)
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

// errBodyTooLargeForQueue — тело влезает в sync-лимит, но не в async: узел на
// паузе, и запрос обязан уехать в очередь (§3.6), а туда такое тело не пройдёт.
// Текст отличается от errBodyTooLarge намеренно: у клиента запрос «того же
// размера, что и минуту назад» вдруг отвергается, и причина — состояние узла,
// а не сам запрос.
var errBodyTooLargeForQueue = errors.New("request body too large for queued delivery: node is paused")

// drainCap — потолок «дренажа» остатка тела при превышении лимита: дочитываем
// и выбрасываем (io.Discard, без памяти) до этого объёма, чтобы запрос завершился
// штатно и ответ 413 дошёл до клиента/прокси, а не превратился в TCP-reset → 502.
// Очень большие тела (> лимит + drainCap) всё равно оборвут соединение — это
// защита от slow/DoS-дренажа.
const drainCap = 8 << 20 // 8 МиБ

// defaultMaxBodyBytes — последний рубеж readBody: потолок, если ему передали
// неположительный лимит (5 МиБ, совпадает с дефолтом receiver.max_body_bytes в
// platform/config).
//
// В боевом пути сюда не попадают: действующий лимит приходит из
// BodyLimitsProvider (§97), а тот разворачивает «не задано» в дефолт домена
// (100 МиБ). Константа осталась страховкой на случай прямого вызова readBody —
// без неё нулевой лимит означал бы «отвергать всё непустое».
const defaultMaxBodyBytes = 5 * 1024 * 1024

func readBody(c *gin.Context, max int) ([]byte, error) {
	if max <= 0 {
		max = defaultMaxBodyBytes
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
	if errors.Is(err, errBodyTooLarge) || errors.Is(err, errBodyTooLargeForQueue) {
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

// classifyDomainError маппит доменную ошибку маршрутизации в HTTP-код,
// человекочитаемое сообщение и код причины для журнала отказов (§94.2).
// internal=true означает «непредвиденная ошибка» (502) — её caller
// дополнительно логирует, а в журнал клиентов она не попадает: это сбой шины.
//
// Причина возвращается ОТСЮДА, а не выводится из статуса: по коду ответа
// «узел выключен» (503) неотличим от сбоя шины, а 400 не говорит, что именно
// было не так с запросом. Общая для sync (replyDomainError) и async
// (replyAsyncError), чтобы коды, тексты и причины не расходились.
func classifyDomainError(err error) (status int, message string, reason domain.RejectReason, internal bool) {
	switch {
	case errors.Is(err, domain.ErrNodeNotFound),
		// §82.3: async-эндпоинт на не-async узле. Наружу неотличимо от «узла
		// нет» — та же линия, что у зеркальной sync-проверки и у pull-узла в
		// handleAuto: факт существования узла не раскрываем. Диагностику даёт
		// warn в RouteAsync и метрика nexus_async_ingress_rejected_total.
		errors.Is(err, domain.ErrNodeNotAsyncIngress):
		return http.StatusNotFound, "node not found", domain.RejectReasonNodeNotFound, false
	case errors.Is(err, domain.ErrNodeDisabled):
		// ТОТ ЖЕ ответ, что у несуществующего узла: выключенный узел наружу не
		// раскрывается (иначе перебором адресов вычисляется, какие интеграции
		// существуют). Разница видна только в журнале §94 — там причина
		// node_disabled, и оператор понимает, что узел надо включить, а не
		// искать.
		return http.StatusNotFound, "node not found", domain.RejectReasonNodeDisabled, false
	case errors.Is(err, domain.ErrNodeMethodNotAllowed):
		return http.StatusMethodNotAllowed, "http method not allowed for this node", domain.RejectReasonMethodNotAllowed, false
	case errors.Is(err, domain.ErrURLParamRequired),
		errors.Is(err, domain.ErrCallbackNotAllowed):
		return http.StatusBadRequest, err.Error(), domain.RejectReasonBadRequest, false
	case errors.Is(err, domain.ErrURLInvalid):
		return http.StatusBadRequest, "target url is invalid", domain.RejectReasonBadRequest, false
	case errors.Is(err, domain.ErrAckRenderFailed):
		// §83: узел настроен on_error=error. Запрос НЕ принят (рендер идёт до
		// публикации в Kafka), поэтому 400 честен: клиенту следует повторить.
		// Не 502: это ошибка настройки узла, а не сбой шины, и в Sentry ей
		// делать нечего — диагностика в warn receiver.async_ack и метрике.
		return http.StatusBadRequest, "ack template render failed", domain.RejectReasonBadRequest, false
	case errors.Is(err, domain.ErrURLNotAllowed):
		return http.StatusForbidden, "target url not in allowlist", domain.RejectReasonURLNotAllowed, false
	case errors.Is(err, domain.ErrLoopDetected):
		// §32: запрос вернулся в шину больше max_hops раз — петля.
		return http.StatusLoopDetected, "loop detected", domain.RejectReasonLoopDetected, false
	case errors.Is(err, domain.ErrAuthHeaderMissing),
		errors.Is(err, domain.ErrAuthHeaderMalformed),
		errors.Is(err, domain.ErrAuthTokenRequired),
		errors.Is(err, domain.ErrUnauthorized):
		return http.StatusUnauthorized, err.Error(), domain.RejectReasonUnauthorized, false
	default:
		return http.StatusBadGateway, "internal routing error", domain.RejectReasonOther, true
	}
}

// statusClientClosedRequest — код nginx 499 «Client Closed Request» (§109.4):
// клиент закрыл соединение, не дождавшись ответа. В net/http константы нет.
const statusClientClosedRequest = 499

// replyClientGone отвечает 499, если запрос оборвал сам клиент: контекст
// входящего запроса отменён, а ошибка маршрутизации — отмена (§109.4). Это не
// сбой шины (раньше — 502 и ERROR в Sentry, NEXUS-S) и не отказ клиенту:
// строка лога — Debug, журнал отказов §94 такой статус пропускает
// (rejectReason). Отмена при живом контексте запроса сюда не попадает и
// остаётся ошибкой. Тела у ответа нет: читать его некому.
//
// Путь узла — как в error-строке (nodeForErrorLog): клиент может уйти ещё во
// время поиска узла, и тогда путь целиком задал он сам — у passthrough-адреса
// за путём узла идут токены ботов, а debug-строке секреты запрещены (§51.9).
func (h *Handler) replyClientGone(c *gin.Context, err error, nodePath, op string) bool {
	if !clientgone.Is(c.Request.Context(), err) {
		return false
	}
	h.logger.Debug("receiver request canceled by client",
		h.logger.Str("node", nodeForErrorLog(c, nodePath)),
		h.logger.Str("op", op),
		h.logger.Str("method", c.Request.Method),
		h.logger.Str("client_ip", c.ClientIP()),
		h.logger.Err(err))
	c.AbortWithStatus(statusClientClosedRequest)
	return true
}

// senderRetryAfterSec — подсказка клиенту в Retry-After при недоступном
// Sender'е (§109.3): рестарт Sender'а после §109.2 занимает секунды.
const senderRetryAfterSec = "5"

// replySenderUnavailable отвечает 503 + Retry-After, если вызов не удалось
// отправить Sender'у: за ready_wait_ms не нашлось готового транспорта, либо
// ожидание выключено и транспорта не было (§109.3, domain.ErrSenderUnavailable).
// Вызов до Sender'а не дошёл — клиенту честно предложить повтор. Раньше это был 502
// «internal routing error» и ERROR в Sentry на каждый запрос окна выката
// (NEXUS-T/S); теперь WARN: сигнал о настоящем падении Sender'а — алерт на его
// недоступность и счётчик nexus_sender_ready_wait_total{outcome="timeout"}. В
// журнал отказов §94 не пишется: это сбой шины, а не отказ клиенту (503 без
// причины rejectReason пропускает).
func (h *Handler) replySenderUnavailable(c *gin.Context, err error, nodePath, op string) bool {
	if !errors.Is(err, domain.ErrSenderUnavailable) {
		return false
	}
	h.logger.Warn("receiver: sender unavailable",
		h.logger.Str("node", nodeForErrorLog(c, nodePath)),
		h.logger.Str("op", op),
		h.logger.Err(err))
	c.Header("Retry-After", senderRetryAfterSec)
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sender unavailable"})
	return true
}

// replyDomainError — sync-ответ об ошибке: {"error": <msg>}.
func (h *Handler) replyDomainError(c *gin.Context, err error, nodePath, op string) {
	if h.replyClientGone(c, err, nodePath, op) || h.replySenderUnavailable(c, err, nodePath, op) {
		return
	}
	status, msg, reason, internal := classifyDomainError(err)
	errNode := nodeForErrorLog(c, nodePath)
	nodePath = nodeForLog(c, nodePath)
	h.markRejected(c, err, reason, internal)
	if internal {
		h.logger.ErrorWithOp("receiver routing failed", err, op,
			h.logger.Str("node", errNode))
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

// nodeLabelSetter — наблюдатель резолва для RouteInput.OnNodeResolved (§101.3):
// кладёт в контекст метки метрик НАЙДЕННОГО узла. Из адреса метки не выводятся:
// у passthrough-узла хвост адреса несёт секреты и не ограничен, а слог в
// legacy-адресе может оказаться частью пути. Путь — без слога команды, как у
// меток Sender'а (иначе in/out одного узла разъезжаются на дашборде, §21).
//
// ID узла кладётся отдельным ключом для журнала отказов (§117.3): в метки
// Prometheus он не идёт — у них своя кардинальность и смысл.
func nodeLabelSetter(c *gin.Context) func(usecase.ResolvedNode) {
	return func(n usecase.ResolvedNode) {
		c.Set(metrics.NodeLabelKey, n.Path)
		c.Set(metrics.TeamLabelKey, n.TeamSlug)
		c.Set(RejectNodeIDKey, n.ID)
	}
}

// nodeForLog — путь узла для строк лога: путь найденного узла, если резолв уже
// состоялся, иначе путь из адреса. Ошибки после резолва уходят и в Sentry, а
// хвост passthrough-адреса несёт секреты; путь из адреса остаётся только у
// отказов до резолва — там он и нужен («почему 404»). Токен бота в нём
// маскируется (§109.6): debug-строке секреты запрещены (§51.9), а форма пути
// для диагностики остаётся.
func nodeForLog(c *gin.Context, fromURL string) string {
	if v := resolvedNodeLabel(c); v != "" {
		return v
	}
	return sensitive.MaskBotTokens(fromURL)
}

// nodeForErrorLog — путь узла для error-строки лога (§101.13). Она уходит и в
// Sentry, а до резолва путь задал клиент: у passthrough-адреса за путём узла
// идут секреты (токены ботов). Поэтому без резолва в строке остаётся только
// первый сегмент адреса — по нему видно, куда стучались, но не хвост.
func nodeForErrorLog(c *gin.Context, fromURL string) string {
	if v := resolvedNodeLabel(c); v != "" {
		return v
	}
	if first, _, more := strings.Cut(fromURL, "/"); more {
		return first + "/…"
	}
	return fromURL
}

// resolvedNodeLabel — путь найденного узла из метки; пусто, пока резолв не
// состоялся.
func resolvedNodeLabel(c *gin.Context) string {
	if v := c.GetString(metrics.NodeLabelKey); v != metrics.NodeUnresolved {
		return v
	}
	return ""
}

// markRejected размечает отказ для наблюдаемости (§94): кладёт код причины в
// контекст — оттуда его берёт RejectLogMiddleware — и подменяет метку узла,
// когда узла не существует.
//
// internal-ошибки (502) не размечаются: это сбой шины, а не отказ клиенту, и в
// журнале отказов ему делать нечего.
//
// Подмена метки касается ТОЛЬКО ErrNodeNotFound без резолва: путь в этом
// случае задал клиент, и метка node у nexus_requests_total росла бы новым
// рядом на каждый мусорный путь (§94.8). Узел, который найден, но отвечает
// «узла нет» (не тот тип за sync-адресом, pull-узел по короткому адресу), метку
// своего узла сохраняет (§101.3) — как и §82.3 (ErrNodeNotAsyncIngress): ряд
// ограничен числом узлов, а оператор видит, в какой узел стучатся не так.
func (h *Handler) markRejected(c *gin.Context, err error, reason domain.RejectReason, internal bool) {
	if internal {
		return
	}
	SetRejectReason(c, reason)
	if errors.Is(err, domain.ErrNodeNotFound) && c.GetString(metrics.NodeLabelKey) == "" {
		c.Set(metrics.NodeLabelKey, metrics.NodeUnresolved)
		c.Set(metrics.TeamLabelKey, "")
	}
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
	if h.replyClientGone(c, err, nodePath, op) {
		return
	}
	status, msg, reason, internal := classifyDomainError(err)
	errNode := nodeForErrorLog(c, nodePath)
	nodePath = nodeForLog(c, nodePath)
	h.markRejected(c, err, reason, internal)
	if internal {
		h.logger.ErrorWithOp("receiver async routing failed", err, op,
			h.logger.Str("node", errNode))
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
	if errors.Is(err, domain.ErrNodeNotAsyncIngress) && h.metrics != nil {
		// §82.3: клиент видит только 404 «node not found», поэтому счётчик —
		// единственный внешний признак рассинхронизации настроек. Warn с
		// root_method и IP пишет RouteAsync: там известен сам узел.
		h.metrics.IncAsyncIngressRejected(metrics.NodeLabelsFromGin(c))
	}
	c.JSON(status, gin.H{"result": false, "message": msg})
}
