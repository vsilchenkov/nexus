package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"nexus/internal/platform/clientgone"
	"nexus/internal/platform/logging"
)

// statusClientClosedRequest — код nginx 499 «Client Closed Request»: клиент
// закрыл соединение, не дождавшись ответа. В net/http константы нет.
const statusClientClosedRequest = 499

// logServerError пишет ошибку обработчика (§109.4). Отмена запроса, который
// оборвал сам клиент (закрыл вкладку, опрос логов прерван навигацией), — не
// сбой сервера: она уходит на Debug, а не в ERROR и Sentry (NEXUS-11/13 —
// «logs.list: scan node: context canceled»). Остальное — прежний ErrorWithOp.
//
// Меняется только уровень строки: ответ обработчик пишет как раньше, клиенту
// он всё равно не доставляется.
func logServerError(c *gin.Context, logger logging.Logger, msg string, err error, op string, attrs ...slog.Attr) {
	if clientgone.Is(c.Request.Context(), err) {
		logger.Debug(msg+": canceled by client", append(attrs, logger.Op(op), logger.Err(err))...)
		return
	}
	logger.ErrorWithOp(msg, err, op, attrs...)
}
