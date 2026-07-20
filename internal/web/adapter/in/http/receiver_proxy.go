package http

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/logging"
)

// RegisterReceiverProxy вешает на Web-роутер реверс-прокси боевых эндпоинтов
// Receiver: /api/v1/request/*, /api/v1/requestAsync/*, /api/v1/callback/*.
//
// Web Service — единый вход (§17.1): клиенты бьют в его хост и для UI, и для
// боевого трафика. Без этого прокси путь /api/v1/request/... не матчился бы ни
// одним handler'ом Web и проваливался в NoRoute. Запросы проксируются «как
// есть» (метод, заголовки, тело, query), а ответ Receiver (тело+код+заголовки)
// возвращается клиенту без изменений.
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
		// Метод и путь (без query — там могут быть токены) обязательны для
		// триажа: голый "EOF" в Sentry не привязать к узлу/запросу
		// (NEXUS-8: EOF'ы оказались таймаутами конкретного узла, вычислять
		// пришлось по совпадению секунд с CH-логом).
		logger.ErrorWithOp("receiver proxy upstream error", perr, "web.proxy",
			logger.Str("method", r.Method),
			logger.Str("path", r.URL.Path))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"receiver unavailable"}`))
	}
	// FlushInterval>0 — корректная потоковая передача ответов получателя.
	proxy.FlushInterval = 100 * time.Millisecond

	h := gin.WrapH(proxy)
	r.Any("/api/v1/request/*path", h)
	r.Any("/api/v1/requestAsync/*path", h)
	r.Any("/api/v1/callback/*path", h)
}
