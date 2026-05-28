// Package telegram — минимальный HTTP-клиент Telegram Bot API для отправки
// уведомлений операторам (§20.5). Без доменной/per-team логики — только
// sendMessage. Используется NotificationScheduler в Web Service.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"nexus/internal/platform/logging"
)

// defaultBaseURL — публичный Telegram Bot API. Переопределяется в тестах.
const defaultBaseURL = "https://api.telegram.org"

// Client отправляет сообщения через Telegram Bot API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	logger     logging.Logger
}

// New создаёт клиент с собственным http.Client (timeout 10s). Не
// переиспользует sender-овский клиент — это другой сервис/назначение.
func New(logger logging.Logger) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    defaultBaseURL,
		logger:     logger,
	}
}

// WithBaseURL переопределяет адрес API (для unit-тестов через httptest).
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

type sendMessagePayload struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Send отправляет text в чат chatID от имени бота token. Возвращает ошибку
// при сетевом сбое, не-200 статусе или `ok:false` в ответе API.
func (c *Client) Send(ctx context.Context, token, chatID, text string) error {
	payload, err := json.Marshal(sendMessagePayload{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	})
	if err != nil {
		return fmt.Errorf("telegram: marshal payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out apiResponse
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		return fmt.Errorf("telegram: api error: status=%d ok=%v desc=%q", resp.StatusCode, out.OK, out.Description)
	}
	return nil
}
