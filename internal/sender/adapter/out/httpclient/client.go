// Package httpclient — outbound HTTP-вызов внешнего узла.
//
// Реализует port.HTTPCaller. Один общий http.Client с пулом соединений
// (keep-alive) на весь Sender — это критично для производительности
// при работе с одним и тем же внешним узлом (§9.3 ТЗ).
package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
	"bus/internal/sender/usecase/port"
)

// Client реализует port.HTTPCaller.
type Client struct {
	hc     *http.Client
	logger logging.Logger
}

var _ port.HTTPCaller = (*Client)(nil)

func New(cfg *config.SenderHTTPClientConfig, logger logging.Logger) *Client {
	transport := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     time.Duration(cfg.IdleConnTimeoutSec) * time.Second,
		TLSHandshakeTimeout: time.Duration(cfg.TLSHandshakeTimeoutMs) * time.Millisecond,
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(cfg.DialTimeoutMs) * time.Millisecond,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.TimeoutMs) * time.Millisecond,
		},
		logger: logger,
	}
}

func (c *Client) Do(ctx context.Context, req *port.HTTPRequest) (*port.HTTPResponse, error) {
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	hreq, err := http.NewRequestWithContext(reqCtx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("build http request: %w", err)
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}

	hresp, err := c.hc.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer hresp.Body.Close()

	body, err := io.ReadAll(hresp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	hdrs := make(map[string]string, len(hresp.Header))
	for k, v := range hresp.Header {
		if len(v) > 0 {
			hdrs[k] = v[0]
		}
	}
	return &port.HTTPResponse{
		StatusCode: int32(hresp.StatusCode),
		Headers:    hdrs,
		Body:       body,
	}, nil
}
