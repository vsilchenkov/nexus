package http

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/clientgone"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/sensitive"
)

// RegisterReceiverProxy вешает на Web-роутер реверс-прокси боевых эндпоинтов
// Receiver — ВСЁ, что приходит на /api/v1/*.
//
// Web Service — единый вход (§17.1): клиенты бьют в его хост и для UI, и для
// боевого трафика. Без этого прокси путь /api/v1/request/... не матчился бы ни
// одним handler'ом Web и проваливался в NoRoute. Запросы проксируются «как
// есть» (метод, заголовки, тело, query), а ответ Receiver (тело+код+заголовки)
// возвращается клиенту без изменений.
//
// §78.2: маршрут один и без разбора формы адреса. Три отдельных
// (request/requestAsync/callback) не дали бы добавить короткую форму §78.1 —
// catch-all рядом с ними роняет gin при старте, — а разбирать первый сегмент
// Web незачем: диспетчеризация целиком на Receiver. Собственных маршрутов под
// /api/v1/ у Web нет (весь REST админки живёт на /api/… без v1), так что
// catch-all тут ничего не перехватывает. Следствие: неизвестный /api/v1/…
// теперь получает 404 от Receiver ({"error":"node not found"}), а не от
// SPA-фолбэка Web ({"error":"not found"}).
//
// receiverURL — базовый адрес Receiver (например, http://receiver:8080).
// При пустом receiverURL прокси не регистрируется (single-process dev без
// отдельного Receiver) — маршруты тогда вернут 404 как раньше.
func RegisterReceiverProxy(r *gin.Engine, receiverURL string, logger logging.Logger) {
	if receiverURL == "" {
		logger.Warn("receiver proxy disabled: web.receiver_url is empty")
		return
	}
	target, err := url.Parse(receiverURL)
	if err != nil {
		logger.ErrorWithOp("receiver proxy disabled: invalid web.receiver_url", err, "web.proxy",
			logger.Str("url", receiverURL))
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	// NewSingleHostReverseProxy джойнит target.Path с req.URL.Path; для
	// receiverURL без пути исходный /api/v1/... уходит в Receiver без изменений.
	// X-Forwarded-For добавляется стандартным Director'ом — Receiver увидит
	// реальный IP клиента (важно для аудита/логов, §3.5).
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, perr error) {
		if clientgone.Is(r.Context(), perr) {
			// §109.4: клиент ушёл, не дождавшись ответа Receiver'а — это не
			// сбой прокси. 499 (как у nginx) — для метрик и спана Sentry.
			logger.Debug("receiver proxy: request canceled by client",
				logger.Str("method", r.Method),
				logger.Str("path", sensitive.MaskBotTokens(r.URL.Path)))
			w.WriteHeader(statusClientClosedRequest)
			return
		}
		// Метод и путь (без query — там могут быть токены) обязательны для
		// триажа: голый "EOF" в Sentry не привязать к узлу/запросу
		// (NEXUS-8: EOF'ы оказались таймаутами конкретного узла, вычислять
		// пришлось по совпадению секунд с CH-логом).
		// Путь passthrough-узла несёт токен бота (§109.6): Sentry маскирует его
		// сам, а stdout-строку — только здесь.
		logger.ErrorWithOp("receiver proxy upstream error", perr, "web.proxy",
			logger.Str("method", r.Method),
			logger.Str("path", sensitive.MaskBotTokens(r.URL.Path)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"receiver unavailable"}`))
	}
	// FlushInterval>0 — корректная потоковая передача ответов получателя.
	proxy.FlushInterval = 100 * time.Millisecond

	// Регистрируется на движке, а не в группе /api: боевой трафик, как и
	// раньше, не проходит CSRF-проверку Origin (см. routes.go).
	r.Any("/api/v1/*path", gin.WrapH(proxy))
}
