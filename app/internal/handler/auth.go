package handler

import (
	"errors"
	"net/http"
	"strings"

	jwtLib "bus/app/internal/lib/jwt"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Middleware для basic auth
func (h Handler) basicAuthMW() gin.HandlerFunc {

	const op = "handler.basicAuthMW"

	return func(c *gin.Context) {
		username, password, ok := c.Request.BasicAuth()
		if !ok {
			newErrorResponse(c, http.StatusUnauthorized, op+".BasicAuth", errors.New("invalid basic auth header"))
			return
		}

		userID, role, err := h.store.VerifyUser(h.ctx, username, password)
		if err != nil {
			newErrorResponse(c, http.StatusUnauthorized, op+".VerifyUser", err)
			return
		}

		c.Set("userId", userID)
		c.Set("userRole", role)

		c.Next()
	}

}

// Middleware для проверки токена
func (h Handler) authMW() gin.HandlerFunc {

	const op = "handler.authMW"

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			newErrorResponse(c, http.StatusUnauthorized, op+".Authorization", errors.New("authorization header required"))
			return
		}

		// Ожидаем формат: "Bearer <token>"
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			newErrorResponse(c, http.StatusUnauthorized, op+".Bearer", errors.New("invalid authorization header format"))
			return
		}

		tokenStr := parts[1]

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
			// Проверка метода подписи
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtLib.JwtSecret(h.config.Authorization.JWT.Secret), nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

		if err != nil || !token.Valid {
			newErrorResponse(c, http.StatusUnauthorized, op+".token.valid", errors.New("invalid token"))
			return
		}

		// Достаём клеймы и кладём, deviceId в контекст
		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if sub, ok := claims["sub"].(string); ok {
				c.Set("deviceId", sub)
			}
		}

		c.Next()
	}
}
