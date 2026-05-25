// Package receiver — HTTP-клиент Web Service к Receiver Service.
// Используется для replay (§7.4.1): Web берёт оригинальный лог из CH,
// формирует request и отправляет через реальный pipeline шины.
package receiver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"bus/internal/platform/logging"
	"bus/internal/web/usecase/port"
)

type HTTPDispatcher struct {
	baseURL string // например, http://receiver:8080
	client  *http.Client
	logger  logging.Logger
}

var _ port.ReceiverDispatcher = (*HTTPDispatcher)(nil)

func NewHTTPDispatcher(baseURL string, timeout time.Duration, logger logging.Logger) *HTTPDispatcher {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPDispatcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
		logger:  logger,
	}
}

func (d *HTTPDispatcher) Dispatch(ctx context.Context, req port.DispatchRequest) (*port.DispatchResponse, error) {
	root := "/v1/request"
	if req.Async {
		root = "/v1/requestAsync"
	}
	target := fmt.Sprintf("%s%s/%s", d.baseURL, root, strings.TrimPrefix(req.NodePath, "/"))
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}

	method := req.Method
	if method == "" {
		method = http.MethodPost
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("Content-Type") == "" && len(req.Body) > 0 {
		// Replay сохраняет оригинальный JSON, по умолчанию JSON.
		var probe any
		if json.Unmarshal(req.Body, &probe) == nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}
	}

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("dispatch to receiver: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	out := &port.DispatchResponse{
		StatusCode: resp.StatusCode,
		Body:       body,
		Headers:    make(map[string]string, len(resp.Header)),
	}
	for k, vv := range resp.Header {
		if len(vv) > 0 {
			out.Headers[k] = vv[0]
		}
	}
	return out, nil
}
