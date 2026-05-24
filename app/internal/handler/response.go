package handler

import (
	"fmt"
	"os"

	"github.com/vsilchenkov/logging"

	"github.com/gin-gonic/gin"
)

type errorResponse struct {
	Message string `json:"message"`
}

func newErrorResponse(c *gin.Context, statusCode int, op string, err error) {

	logger := logging.GetLogger()
	if logger != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "Error response (logger panic): Request=%s op=%s err=%v recovered=%v\n", c.Request.RequestURI, op, err, r)
				}
			}()
			// Логируем в зависимости от типа ошибки
			switch {
			case statusCode >= 500:
				// 5xx - серверные ошибки, всегда логируем как Error
				logger.Error("Server error",
					logger.Str("Request", c.Request.RequestURI),
					logger.Any("StatusCode", statusCode),
					logger.Op(op),
					logger.Err(err))
			case statusCode == 409:
				// Ошибки устройств
				logger.Warn("Device error",
					logger.Str("Request", c.Request.RequestURI),
					logger.Any("StatusCode", statusCode),
					logger.Op(op),
					logger.Err(err))
			case statusCode == 401 || statusCode == 403:
				// Ошибки авторизации
				logger.Debug("Auth error",
					logger.Str("Request", c.Request.RequestURI),
					logger.Any("StatusCode", statusCode),
					logger.Op(op),
					logger.Err(err))
			case statusCode == 404:
				// Not Found - Debug или вообще не логируем
				logger.Debug("Resource not found",
					logger.Str("Request", c.Request.RequestURI),
					logger.Op(op),
					logger.Err(err))
			case statusCode >= 400:
				// Остальные клиентские ошибки - Info или Warn
				logger.Info("Client error",
					logger.Str("Request", c.Request.RequestURI),
					logger.Any("StatusCode", statusCode),
					logger.Op(op),
					logger.Err(err))
			}
		}()
	} else {
		fmt.Fprintf(os.Stderr, "Error response: Request=%s op=%s err=%v\n", c.Request.RequestURI, op, err)
	}

	c.AbortWithStatusJSON(statusCode, errorResponse{err.Error()})
}
