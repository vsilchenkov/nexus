// Package instanceprobe — проверка доступности соседнего инстанса Nexus (§73.2).
//
// Проба ходит на ПУБЛИЧНЫЕ эндпоинты соседа и токена не требует: GET /api/version
// и GET /ready зарегистрированы вне auth-группы на каждой ноде. Поэтому на
// удалённой стороне для §73 ничего менять не нужно — вся работа здесь.
//
// Опрашивает именно сервер, а не браузер: CORS в проекте не настроен, а CSP
// задаёт connect-src 'self', так что запрос из SPA на соседний хост был бы
// заблокирован. Побочно снимается и mixed-content (https-SPA → http-нода).
package instanceprobe

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

const (
	// maxBodyBytes — потолок чтения ответа соседа. Оба эндпоинта отдают
	// компактный JSON; лимит защищает от адреса, по которому стоит не Nexus, а
	// что-то отдающее гигабайты.
	maxBodyBytes = 64 << 10
	// defaultTimeout — таймаут на КАЖДЫЙ из двух запросов, если конфиг не задан.
	defaultTimeout = 3 * time.Second

	versionPath = "/api/version"
	readyPath   = "/ready"
)

// Prober — HTTP-реализация port.InstanceProber.
type Prober struct {
	client *http.Client
	logger logging.Logger
}

var _ port.InstanceProber = (*Prober)(nil)

// New создаёт пробер с общим для обоих запросов таймаутом.
//
// Редиректы намеренно не выполняются (CheckRedirect возвращает
// ErrUseLastResponse): следование за 30x увело бы серверный запрос на адрес,
// которого администратор не вводил и который не проходил валидацию. Ответ 30x
// трактуется как «по адресу не Nexus» — статус error.
func New(timeout time.Duration, logger logging.Logger) *Prober {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Prober{
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger: logger,
	}
}

// versionPayload — подмножество ответа GET /api/version, которое нужно реестру.
// Остальные поля (commit, build_date, override_allowed) в интерфейсе списка не
// показываются и намеренно не читаются.
type versionPayload struct {
	Version  string `json:"version"`
	Instance string `json:"instance"`
}

// readyPayload — подмножество ответа GET /ready. Карта checks не читается: она
// содержит тексты ошибок с внутренними адресами и портами соседа, и её место —
// в его собственном интерфейсе, а не в чужом списке инстансов.
type readyPayload struct {
	Ready    bool `json:"ready"`
	Degraded bool `json:"degraded"`
}

// readyOutcome — что сказал /ready. readyUnknown означает «спросить не вышло»
// (например, у соседа старая сборка без эндпоинта) и статус не понижает.
type readyOutcome int

const (
	readyUnknown readyOutcome = iota
	readyOK
	readyDegraded
)

// probeFailure — неуспех пробы с уже определённым статусом и короткой причиной.
type probeFailure struct {
	status domain.PeerInstanceStatus
	reason string
}

func (f *probeFailure) Error() string { return f.reason }

// Probe опрашивает соседа: /api/version даёт версию и код инстанса, /ready
// уточняет деградацию. Запросы идут параллельно — в успешном случае (он же
// самый частый) проба укладывается в один таймаут вместо двух.
//
// Ошибка наружу не возвращается: недоступность соседа — штатный исход.
func (p *Prober) Probe(ctx context.Context, baseURL string) port.InstanceProbeResult {
	var (
		wg    sync.WaitGroup
		ready readyOutcome
	)
	wg.Go(func() {
		// Recover внутри функции, а не снаружи: wg.Go делает Done своим defer'ом
		// уже ЗА её пределами, поэтому паника гасится здесь — до того, как
		// счётчик группы будет уменьшен, и не роняет процесс (§30.2).
		defer safego.Recover(p.logger, "web.instanceprobe.ready")
		ready = p.fetchReady(ctx, baseURL)
	})

	start := time.Now()
	ver, err := p.fetchVersion(ctx, baseURL)
	latency := int(time.Since(start).Milliseconds())
	wg.Wait()

	if err != nil {
		var f *probeFailure
		if !errors.As(err, &f) {
			// Сюда попасть нельзя: fetchVersion возвращает только probeFailure.
			// Ветка оставлена, чтобы будущая правка не превратила незнакомую
			// ошибку в «активен».
			f = &probeFailure{status: domain.PeerInstanceError, reason: "request failed"}
		}
		p.logger.Debug("instance probe failed",
			p.logger.Str("base_url", baseURL),
			p.logger.Str("status", string(f.status)),
			p.logger.Str("reason", f.reason),
			p.logger.Int("duration_ms", latency))
		return port.InstanceProbeResult{Status: f.status, Error: f.reason}
	}

	res := port.InstanceProbeResult{
		Status:     domain.PeerInstanceActive,
		Version:    ver.Version,
		InstanceID: ver.Instance,
		LatencyMS:  &latency,
	}
	if ready == readyDegraded {
		res.Status = domain.PeerInstanceDegraded
	}
	p.logger.Debug("instance probe ok",
		p.logger.Str("base_url", baseURL),
		p.logger.Str("status", string(res.Status)),
		p.logger.Str("version", res.Version),
		p.logger.Str("instance", res.InstanceID),
		p.logger.Int("duration_ms", latency))
	return res
}

// fetchVersion запрашивает GET {base}/api/version. Любой неуспех возвращается
// как *probeFailure с уже выбранным статусом.
func (p *Prober) fetchVersion(ctx context.Context, baseURL string) (versionPayload, error) {
	var out versionPayload
	body, err := p.get(ctx, baseURL+versionPath)
	if err != nil {
		return out, err
	}
	if jsonErr := json.Unmarshal(body, &out); jsonErr != nil {
		// По адресу отвечает не Nexus (HTML страницы входа прокси и т.п.).
		// Текст ошибки парсера не пробрасываем: он вклеивает в сообщение кусок
		// самого тела (грабли §68).
		return versionPayload{}, &probeFailure{status: domain.PeerInstanceError, reason: "invalid response"}
	}
	if out.Version == "" {
		// JSON разобрался, но это не ответ /api/version — например, чужой
		// сервис вернул {} или страницу-заглушку в JSON.
		return versionPayload{}, &probeFailure{status: domain.PeerInstanceError, reason: "invalid response"}
	}
	return out, nil
}

// fetchReady запрашивает GET {base}/ready. Ошибки не поднимает: эндпоинт
// уточняющий, и его недоступность не должна отменять успешно полученную версию.
func (p *Prober) fetchReady(ctx context.Context, baseURL string) readyOutcome {
	body, err := p.get(ctx, baseURL+readyPath)
	if err != nil {
		var f *probeFailure
		// 503 от /ready — это не сбой пробы, а штатный сигнал «упала
		// обязательная зависимость» (healthcheck.Ready отдаёт именно его).
		if errors.As(err, &f) && f.reason == reasonHTTPStatus(http.StatusServiceUnavailable) {
			p.logger.Debug("instance ready reports unavailable",
				p.logger.Str("base_url", baseURL))
			return readyDegraded
		}
		p.logger.Debug("instance ready probe skipped",
			p.logger.Str("base_url", baseURL),
			p.logger.Str("reason", errReason(err)))
		return readyUnknown
	}
	var payload readyPayload
	if jsonErr := json.Unmarshal(body, &payload); jsonErr != nil {
		p.logger.Debug("instance ready probe returned non-json",
			p.logger.Str("base_url", baseURL))
		return readyUnknown
	}
	if !payload.Ready || payload.Degraded {
		return readyDegraded
	}
	return readyOK
}

// get выполняет GET и возвращает тело, ограниченное maxBodyBytes.
func (p *Prober) get(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &probeFailure{status: domain.PeerInstanceError, reason: "invalid address"}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, &probeFailure{status: domain.PeerInstanceUnreachable, reason: transportReason(err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &probeFailure{status: domain.PeerInstanceError, reason: reasonHTTPStatus(resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, &probeFailure{status: domain.PeerInstanceError, reason: "invalid response"}
	}
	return body, nil
}

// reasonHTTPStatus — единый формат причины по коду ответа. Через функцию, а не
// строкой в двух местах: fetchReady сравнивает результат с этим же значением.
func reasonHTTPStatus(code int) string {
	return "http " + strconv.Itoa(code)
}

// transportReason классифицирует сетевую ошибку в короткую причину.
//
// Классификация идёт по ТИПАМ ошибок (errors.As/errors.Is), а не по подстрокам
// в тексте: сообщения стандартной библиотеки не являются контрактом, а ещё они
// вклеивают в себя адрес и порт — такой текст нельзя класть в last_error.
func transportReason(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns error"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "tls error"
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return "tls error"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connection failed"
	}
	return "request failed"
}

// errReason достаёт причину из *probeFailure для debug-лога.
func errReason(err error) string {
	var f *probeFailure
	if errors.As(err, &f) {
		return f.reason
	}
	return "request failed"
}
