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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	otelpf "nexus/internal/platform/otel"
	"nexus/internal/sender/usecase/port"
)

// maxRedirects — как в дефолте net/http: следуем не дальше 10 переходов (§50).
const maxRedirects = 10

// Client реализует port.HTTPCaller.
type Client struct {
	hc      *http.Client
	logger  logging.Logger
	maxResp int // транспортный лимит тела ответа (байт); 0 = без лимита.
}

var _ port.HTTPCaller = (*Client)(nil)

// New создаёт HTTP-клиент. maxResponseBytes — лимит размера тела ОТВЕТА (байт),
// = sender.grpc_max_message_bytes минус запас под envelope: чтение оборвётся на
// нём (io.LimitReader), тело не дочитается в память (§43-rev, memory-safe). 0 =
// без лимита.
func New(cfg *config.SenderHTTPClientConfig, logger logging.Logger, maxResponseBytes int) *Client {
	transport := &http.Transport{
		MaxIdleConns:        cfg.MaxIdleConns,
		MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
		IdleConnTimeout:     time.Duration(cfg.IdleConnTimeoutSec) * time.Second,
		TLSHandshakeTimeout: time.Duration(cfg.TLSHandshakeTimeoutMs) * time.Millisecond,
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(cfg.DialTimeoutMs) * time.Millisecond,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.TimeoutMs) * time.Millisecond,
		},
		logger:  logger,
		maxResp: maxResponseBytes,
	}
}

func (c *Client) Do(ctx context.Context, req *port.HTTPRequest) (*port.HTTPResponse, error) {
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// OTel client-span + W3C traceparent injection (§16, Phase 8.3). При
	// Enable=false — оба no-op без накладных расходов.
	var spanFinish func(statusCode int, err error)
	reqCtx, spanFinish = otelpf.StartHTTPClientSpan(reqCtx, req.Method, req.URL)

	hreq, err := http.NewRequestWithContext(reqCtx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		spanFinish(0, err)
		return nil, fmt.Errorf("build http request: %w", err)
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	otelpf.InjectHTTPHeaders(reqCtx, hreq.Header)

	// §50: следуем 3xx-редиректам (как дефолт Go), но логируем каждый переход и
	// накапливаем хопы для reason лога. Копия клиента на вызов — Transport общий
	// (пул соединений не рвётся), закрытие захватывает node path и срез хопов.
	var hops []port.RedirectHop
	hc := *c.hc
	hc.CheckRedirect = func(nextReq *http.Request, via []*http.Request) error {
		hop := c.redirectHop(nextReq, via)
		hops = append(hops, hop)
		c.logRedirect(req.NodePath, hop)
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}

	hresp, err := hc.Do(hreq)
	if err != nil {
		spanFinish(0, err)
		return nil, err
	}
	defer hresp.Body.Close()

	// §43-rev: читаем тело ответа с лимитом (maxResp+1), чтобы НЕ затягивать в
	// память гигантский ответ (защита от OOM). Если перевалили за лимит — отдаём
	// TooLarge и пустое тело; вызывающая сторона вернёт клиенту 502.
	reader := io.Reader(hresp.Body)
	if c.maxResp > 0 {
		reader = io.LimitReader(hresp.Body, int64(c.maxResp)+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		spanFinish(hresp.StatusCode, err)
		return nil, fmt.Errorf("read response body: %w", err)
	}
	spanFinish(hresp.StatusCode, nil)

	hdrs := make(map[string]string, len(hresp.Header))
	for k, v := range hresp.Header {
		if len(v) > 0 {
			hdrs[k] = v[0]
		}
	}
	if c.maxResp > 0 && len(body) > c.maxResp {
		// Тело больше лимита — не держим его в памяти и не отдаём дальше.
		return &port.HTTPResponse{
			StatusCode: int32(hresp.StatusCode),
			Headers:    hdrs,
			TooLarge:   true,
			Redirects:  hops,
		}, nil
	}
	return &port.HTTPResponse{
		StatusCode: int32(hresp.StatusCode),
		Headers:    hdrs,
		Body:       body,
		Redirects:  hops,
	}, nil
}

// redirectHop строит запись о переходе 3xx (§50): via[len-1] — запрос, вызвавший
// редирект; nextReq — следующий. nextReq.Response — 3xx-ответ, породивший переход.
func (c *Client) redirectHop(nextReq *http.Request, via []*http.Request) port.RedirectHop {
	prev := via[len(via)-1]
	status := 0
	if nextReq.Response != nil {
		status = nextReq.Response.StatusCode
	}
	return port.RedirectHop{
		Status:       status,
		From:         redactURL(prev.URL),
		To:           redactURL(nextReq.URL),
		FromMethod:   prev.Method,
		ToMethod:     nextReq.Method,
		SchemeChange: schemeChange(prev.URL, nextReq.URL),
	}
}

// logRedirect пишет служебный лог перехода. Warn — если сменился метод
// (301/302/303 POST→GET молча теряет тело запроса, реальный риск); иначе Info.
func (c *Client) logRedirect(nodePath string, hop port.RedirectHop) {
	attrs := []slog.Attr{
		c.logger.Str("node", nodePath),
		c.logger.Int("status", hop.Status),
		c.logger.Str("from", hop.From),
		c.logger.Str("to", hop.To),
		c.logger.Str("from_method", hop.FromMethod),
		c.logger.Str("to_method", hop.ToMethod),
		c.logger.Str("scheme_change", hop.SchemeChange),
	}
	if hop.FromMethod != hop.ToMethod {
		c.logger.Warn("sender redirect changed method (request body dropped)", attrs...)
	} else {
		c.logger.Info("sender follows external redirect", attrs...)
	}
}

// redactURL — scheme://host/path без query: в query бывают токены, а логи и
// Sentry query не маскируют (зеркалит otel.sanitizeURL). nil → "".
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// schemeChange классифицирует смену схемы between from→to.
func schemeChange(from, to *url.URL) string {
	if from == nil || to == nil || from.Scheme == to.Scheme {
		return "same"
	}
	if from.Scheme == "http" && to.Scheme == "https" {
		return "upgrade"
	}
	if from.Scheme == "https" && to.Scheme == "http" {
		return "downgrade"
	}
	return "same"
}
