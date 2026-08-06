// Package usecase — бизнес-логика Sender Service.
package usecase

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// SendInput — параметры одного outbound-вызова.
type SendInput struct {
	ID         string
	NodePath   string
	NodeID     string // §37: UUID узла → колонка node_id лога (per-node атрибуция)
	RootMethod domain.RootMethod

	TargetURL string
	Method    string // HTTP-глагол (GET/POST/…) → колонка http_method
	// RequestPath — §39: подпуть запроса (хвост path-passthrough) → колонка
	// `method` лог-таблицы. Пусто у обычных узлов. На сам HTTP-вызов не влияет.
	RequestPath string
	Headers     map[string]string
	Body        []byte

	TimeoutMs      int32
	RetryCount     int32
	RetryBackoffMs int32

	ClickHouseTable string
	LogRequestBody  bool
	LogResponseBody bool
	LogHeaders      bool
	ClientIP        string

	// §22: контроль логирования узла.
	LoggingEnabled     bool  // false → лог в ClickHouse не пишется совсем
	MaxBodySizeEnabled bool  // включает обрезку сохраняемых тел
	MaxBodySize        int32 // макс. число символов (рун) в request/response

	// §81.3: политика circuit breaker'а узла (0 = глобальная из конфигурации
	// Sender'а). Async берёт её из БД вместе с узлом, sync получает по gRPC.
	BreakerThreshold   int32
	BreakerCooldownSec int32

	// §55: вызов тестовый (dry-run из UI). HTTP-запрос выполняется по-настоящему
	// и тем же клиентом, но следов на узле не остаётся: ни лога в ClickHouse, ни
	// метрик/статуса (гасятся в gRPC-адаптере), ни участия в circuit breaker.
	// Иначе тест по мёртвому адресу открыл бы breaker живого узла и тот начал бы
	// отдавать 503 боевому трафику.
	DryRun bool
}

// SendOutput — результат, который Sender отдаёт обратно Receiver'у.
type SendOutput struct {
	StatusCode int32
	Headers    map[string]string
	Body       []byte
	Error      string
	// Timeout — внешний узел не ответил именно по таймауту (истёк per-node
	// timeout_ms), а не по отказу соединения/DNS. Receiver превращает это в 504
	// вместо 502 (см. proto SendResponse.timeout).
	Timeout bool
	// ParentGone — родительский контекст вызова умер до ответа (§81.2), то есть
	// оборвал вызов НЕ приёмник. Причин несколько — ушёл клиент шины, встал сам
	// сервис, порвался транспорт Receiver→Sender; полный список и почему они не
	// различаются — в godoc domain.ReasonClientCanceled.
	//
	// О здоровье узла это не говорит ничего, поэтому исход последнего вызова
	// (§41/§52: гаудж и персистентный бейдж) по такому вызову НЕ обновляется:
	// иначе узел, чьих ответов не дожидаются, вечно горел бы «Down» — ровно та
	// картина, из-за которой §81 и появился.
	ParentGone bool
	Attempts   int32
	DurationMs int32
}

// CircuitBreaker — interface circuit breaker'а (§9.5 ТЗ).
// Объявлен здесь, чтобы usecase не зависел от Redis-реализации.
// Реализация — internal/platform/circuitbreaker.
type CircuitBreaker interface {
	Allow(ctx context.Context, key string) (bool, error)
	RecordSuccess(ctx context.Context, key string) error
	// §81.3: политика узла (0 = глобальная из конфигурации). Порог применяется
	// в момент учёта, а не при создании breaker'а, потому что у каждого узла он
	// свой, а breaker в процессе один.
	RecordFailure(ctx context.Context, key string, p domain.BreakerPolicy) error
}

// noopBreaker используется, если CB отключён (cfg-зависимость не настроена).
type noopBreaker struct{}

func (noopBreaker) Allow(context.Context, string) (bool, error) { return true, nil }
func (noopBreaker) RecordSuccess(context.Context, string) error { return nil }
func (noopBreaker) RecordFailure(context.Context, string, domain.BreakerPolicy) error {
	return nil
}

// HostResolver — неблокирующий lookup PTR-имени клиента (§67, колонка
// client_host). Контракт: значение возвращается МГНОВЕННО из кеша или "";
// промах планирует фоновый резолв — имя получат следующие записи этого IP.
// Объявлен здесь (консьюмер-сайд, как CircuitBreaker), чтобы usecase не
// зависел от Redis/DNS-реализации. Реализация — internal/platform/rdns.
type HostResolver interface {
	Lookup(ip string) string
}

// noopHostResolver — резолв выключен (sender.rdns.disabled) или не настроен.
type noopHostResolver struct{}

func (noopHostResolver) Lookup(string) string { return "" }

// SendUsecase — оркестрация: HTTP-вызов с retry + асинхронная запись лога в ClickHouse.
type SendUsecase struct {
	httpc  port.HTTPCaller
	logw   port.LogWriter
	cb     CircuitBreaker
	hosts  HostResolver
	logger logging.Logger
	host   string
	// maxResponseBytes — транспортный лимит тела ответа (config
	// grpc_max_message_bytes) для текста reason при 502. Само ограничение чтения
	// делает httpclient (§43-rev).
	maxResponseBytes int
	// jitter возвращает случайную величину в [0,n) для full-jitter backoff.
	// Зависимость, а не прямой rand.Intn (§4 CLAUDE.md): иначе тест ретраев
	// не может предсказать паузу и вынужден либо спать, либо не проверять её.
	jitter func(n int) int
}

// SendOption — функциональная опция конструктора SendUsecase.
type SendOption func(*SendUsecase)

// WithHostResolver задаёт резолвер PTR-имени клиента (§67). nil игнорируется
// (остаётся noop).
func WithHostResolver(hr HostResolver) SendOption {
	return func(u *SendUsecase) {
		if hr != nil {
			u.hosts = hr
		}
	}
}

// WithJitter подменяет источник случайности backoff'а (§4 CLAUDE.md). В проде
// не используется — только тесты, которым нужна предсказуемая пауза.
func WithJitter(f func(n int) int) SendOption {
	return func(u *SendUsecase) {
		if f != nil {
			u.jitter = f
		}
	}
}

func NewSendUsecase(httpc port.HTTPCaller, logw port.LogWriter, cb CircuitBreaker, logger logging.Logger, maxResponseBytes int, opts ...SendOption) *SendUsecase {
	if cb == nil {
		cb = noopBreaker{}
	}
	host, _ := os.Hostname()
	u := &SendUsecase{
		httpc: httpc, logw: logw, cb: cb, hosts: noopHostResolver{}, logger: logger,
		host: host, maxResponseBytes: maxResponseBytes, jitter: rand.Intn,
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

type attempt struct {
	N               int32     `json:"n"`
	StartedAt       time.Time `json:"started_at"`
	DurationMs      int32     `json:"duration_ms"`
	Status          int32     `json:"status"`
	Reason          string    `json:"reason"`
	BackoffBeforeMs int32     `json:"backoff_before_ms"`
}

// sleepCtx выжидает d, но досрочно возвращает false, если контекст умер раньше.
// true — пауза выдержана полностью.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (u *SendUsecase) Send(ctx context.Context, in SendInput) SendOutput {
	t0 := time.Now()
	rec := &domain.LogRecord{
		ID:   in.ID,
		Type: in.RootMethod,
		URL:  in.TargetURL,
		// §39 кросс-маппинг: HTTP-глагол (in.Method) → колонка http_method;
		// подпуть запроса (in.RequestPath) → колонка method.
		HTTPMethod:      in.Method,
		Method:          in.RequestPath,
		Parameters:      extractQuery(in.TargetURL),
		ChecksumRequest: md5hex(in.Body),
		// §42-доп: истинный размер тела в байтах — как checksum, по ПОЛНОМУ
		// телу до truncateRunes ниже и независимо от LogRequestBody.
		RequestSize: int64(len(in.Body)),
		DateCreate:  t0,
		DateRequest: t0,
		Host:        u.host,
		IP:          in.ClientIP,
		// §67: PTR-имя клиента из кеша (0 мс; промах → "" + фоновый резолв).
		ClientHost: u.hosts.Lookup(in.ClientIP),
		NodeID:     in.NodeID,
	}
	// §22.2/§68: сохраняемая в лог копия тела запроса режется по per-node
	// max_body_size (в рунах); для multipart вместо тела пишется §68-плейсхолдер.
	// checksum и размер считаются по ПОЛНОМУ телу (выше). На сам запрос к внешнему
	// узлу и на ответ клиенту это НЕ влияет.
	if in.LogRequestBody {
		rec.Request = u.logBodyCopy("request", headerGet(in.Headers, "Content-Type"), in.Body, in)
	}

	req := &port.HTTPRequest{
		Method:    in.Method,
		URL:       in.TargetURL,
		NodePath:  in.NodePath, // §50: только для служебного лога редиректов
		Headers:   in.Headers,
		Body:      in.Body,
		TimeoutMs: in.TimeoutMs,
	}

	// Circuit breaker per node (§9.5). Если breaker open — сразу
	// 503 без попытки + лог. Это снижает нагрузку на проблемный
	// внешний узел и ускоряет fail-fast в DLQ для async.
	//
	// §55: dry-run breaker не трогает вообще. Ни Allow (иначе тест конфига
	// упирался бы в чужой открытый breaker и не показал бы реальную причину),
	// ни Record* ниже (иначе серия тестов по мёртвому адресу открыла бы breaker
	// живого узла и тот начал бы отдавать 503 боевому трафику).
	allowed := true
	if !in.DryRun {
		var cbErr error
		allowed, cbErr = u.cb.Allow(ctx, in.NodePath)
		if cbErr != nil {
			// §51.9: ошибка breaker'а (Redis) раньше проглатывалась в «_» —
			// fail-open невидим. Allow при ошибке разрешает вызов, фиксируем след.
			u.logger.Debug("send: circuit breaker check failed (fail-open)",
				u.logger.Str("node", in.NodePath), u.logger.Err(cbErr))
		}
	}
	if !allowed {
		u.logger.Debug("send: circuit breaker open, fail-fast 503",
			u.logger.Str("id", in.ID),
			u.logger.Str("node", in.NodePath))
		rec.DateResponse = time.Now()
		rec.Duration = int32(time.Since(t0).Milliseconds())
		rec.Status = 0
		rec.Done = false
		rec.Reason = domain.ReasonCircuitBreakerOpen
		rec.Attempts = 0
		if in.LoggingEnabled && !in.DryRun {
			u.logw.Write(ctx, in.ClickHouseTable, rec)
		}
		return SendOutput{
			StatusCode: 503,
			Error:      "circuit breaker open",
			DurationMs: rec.Duration,
		}
	}

	var (
		resp      *port.HTTPResponse
		lastErr   error
		attempts  []attempt
		backoffMs int32
	)
	maxAttempts := max(in.RetryCount+1, 1)
	for n := int32(1); n <= maxAttempts; n++ {
		// Пауза перед повтором прерывается смертью контекста. Иначе отмена,
		// случившаяся ВО ВРЕМЯ сна, всё равно оплачивалась бы полным backoff'ом
		// (у долгих узлов — десятки секунд) и лишней заведомо провальной попыткой.
		if backoffMs > 0 && !sleepCtx(ctx, time.Duration(backoffMs)*time.Millisecond) {
			u.logger.Debug("send: context done during backoff, retries stopped",
				u.logger.Str("id", in.ID),
				u.logger.Str("node", in.NodePath),
				u.logger.Int("attempt", int(n)),
				u.logger.Int("backoff_ms", int(backoffMs)),
				u.logger.Err(ctx.Err()))
			break
		}
		started := time.Now()
		resp, lastErr = u.httpc.Do(ctx, req)
		dur := int32(time.Since(started).Milliseconds())

		a := attempt{N: n, StartedAt: started, DurationMs: dur, BackoffBeforeMs: backoffMs}
		if lastErr != nil {
			a.Status = 0
			// §81.2.1: тот же маркер, что и в reason записи — иначе сырой текст с
			// адресом утёк бы в колонку attempts_details мимо нормализации.
			a.Reason = failureReason(ctx, lastErr, in.TimeoutMs, dur)
		} else {
			a.Status = resp.StatusCode
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				a.Reason = "OK"
			} else {
				a.Reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
		}
		attempts = append(attempts, a)
		// §51.9: детали попыток раньше были видны только в CH (attempts_details,
		// и то не всегда) — на debug каждая попытка с исходом и backoff.
		u.logger.Debug("send: attempt finished",
			u.logger.Str("id", in.ID),
			u.logger.Str("node", in.NodePath),
			u.logger.Int("attempt", int(n)),
			u.logger.Int("max_attempts", int(maxAttempts)),
			u.logger.Int("status", int(a.Status)),
			u.logger.Str("reason", a.Reason),
			u.logger.Int("duration_ms", int(dur)),
			u.logger.Int("backoff_before_ms", int(a.BackoffBeforeMs)))

		if lastErr == nil && resp != nil && resp.StatusCode < 500 {
			break // успех или 4xx — retry не помогает
		}
		// Контекст мёртв (клиент разорвал соединение — боевой сценарий «1С сдалась
		// по своему таймауту», либо shutdown сервиса) — ретраить некуда: каждая
		// следующая попытка падает мгновенно тем же `context canceled`, раздувая
		// attempts в логе и подменяя первую — настоящую — причину. Спать в backoff
		// тем более нельзя. Выходим с последней ошибкой.
		if ctxErr := ctx.Err(); ctxErr != nil {
			u.logger.Debug("send: context done, retries stopped",
				u.logger.Str("id", in.ID),
				u.logger.Str("node", in.NodePath),
				u.logger.Int("attempt", int(n)),
				u.logger.Int("max_attempts", int(maxAttempts)),
				u.logger.Err(ctxErr))
			break
		}
		// exponential backoff with full jitter
		base := in.RetryBackoffMs
		if base <= 0 {
			base = 100
		}
		backoffMs = int32(u.jitter(int(base * (1 << min(int(n-1), 5)))))
	}

	out := SendOutput{
		Attempts:   int32(len(attempts)),
		DurationMs: int32(time.Since(t0).Milliseconds()),
	}
	rec.DateResponse = time.Now()
	rec.Duration = out.DurationMs
	rec.Attempts = out.Attempts

	switch {
	case lastErr != nil:
		// §81.2.1: стабильный маркер вместо сырого текста stdlib — тот нестабилен
		// и тащит в журнал полный адрес (у динамического URL — собранный из
		// входящего запроса, см. §68).
		out.Error = failureReason(ctx, lastErr, in.TimeoutMs, out.DurationMs)
		out.ParentGone = ctx.Err() != nil
		// errors.Is, а не разбор текста: http.Client оборачивает дедлайн
		// контекста в *url.Error, цепочка Unwrap сохраняется.
		out.Timeout = errors.Is(lastErr, context.DeadlineExceeded)
		rec.Status = 0
		rec.Done = false
		rec.Reason = out.Error
	case resp != nil && resp.TooLarge:
		// §43-rev: тело ответа превысило ТРАНСПОРТНЫЙ лимит (config
		// grpc_max_message_bytes) — httpclient оборвал чтение, тело не в памяти.
		// Клиенту 502, лог done=0 + reason; тела и checksum нет (не дочитано).
		// §42-доп: ResponseSize остаётся 0 — истинный размер неизвестен.
		out.StatusCode = 502
		out.Error = fmt.Sprintf("response body exceeds transport limit: > %d bytes", u.maxResponseBytes)
		rec.Status = 502
		rec.Done = false
		rec.Reason = out.Error
	case resp != nil:
		out.StatusCode = resp.StatusCode
		out.Headers = resp.Headers
		out.Body = resp.Body
		rec.Status = resp.StatusCode
		rec.Done = resp.StatusCode >= 200 && resp.StatusCode < 300
		rec.ChecksumResponse = md5hex(resp.Body)
		// §42-доп: истинный размер тела ответа в байтах — по полному телу до
		// truncateRunes ниже и независимо от LogResponseBody.
		rec.ResponseSize = int64(len(resp.Body))
		// §22.2/§68: лог-копия ответа режется по per-node max_body_size; для
		// multipart-ответа (multipart/mixed и пр.) вместо тела — §68-плейсхолдер.
		// checksum/размер по полному телу. Клиент получает полный resp.Body (выше).
		if in.LogResponseBody {
			rec.Response = u.logBodyCopy("response", headerGet(resp.Headers, "Content-Type"), resp.Body, in)
		}
		if !rec.Done {
			rec.Reason = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			rec.Reason = "OK"
		}
	}

	// §50: если httpclient следовал 3xx-редиректам — дописываем это в reason
	// лога, чтобы в UI (деталь строки, «Причина») было видно, что фактический
	// адрес отличается от target_url узла (частая причина «поставили http, а
	// идёт https»). Одна запись лога на запрос — редиректы внутри одного вызова.
	if resp != nil && len(resp.Redirects) > 0 {
		rec.Reason = appendRedirectNote(rec.Reason, resp.Redirects)
	}

	u.voteBreaker(ctx, in, resp, lastErr, rec)

	if len(attempts) > 1 || !rec.Done {
		if data, err := json.Marshal(attempts); err == nil {
			rec.AttemptsDetails = string(data)
		}
	}

	// §55: !DryRun — второй гейт поверх LoggingEnabled. Тестовый вызов не пишет в
	// production-таблицу НИКОГДА, даже если логирование узла включено в форме.
	if in.LoggingEnabled && !in.DryRun {
		u.logw.Write(ctx, in.ClickHouseTable, rec)
	}
	return out
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// headerGet возвращает значение заголовка без учёта регистра ключа. Заголовки в
// map обычно уже каноничны (route.go/envelope.go/puller.go кладут "Content-Type"),
// но полагаться на это хрупко — поэтому fallback по EqualFold.
func headerGet(h map[string]string, name string) string {
	if v, ok := h[name]; ok {
		return v
	}
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// logBodyCopy готовит копию тела для сохранения в лог (§22.2/§68): для
// multipart-тела (Content-Type multipart/*) вложения в ClickHouse НЕ пишутся —
// вместо тела кладётся §68-плейсхолдер со сводкой частей, и per-node
// max_body_size на него не действует (он короткий); для обычного тела —
// усечённая по max_body_size копия. kind ("request"/"response") — только для
// debug-следа (§51.9). Не меняет checksum/размер — их считает вызывающая сторона
// по полному телу.
func (u *SendUsecase) logBodyCopy(kind, contentType string, body []byte, in SendInput) string {
	if domain.IsMultipartMediaType(contentType) {
		u.logger.Debug("send: multipart "+kind+", log body replaced with placeholder",
			u.logger.Str("id", in.ID),
			u.logger.Str("node", in.NodePath),
			u.logger.Int("body_bytes", len(body)))
		return domain.MultipartLogPlaceholder(contentType, body)
	}
	return truncateRunes(string(body), in.MaxBodySizeEnabled, in.MaxBodySize)
}

// truncationMarker дописывается к сохраняемому телу, если оно было обрезано по
// max_body_size (§22.2). Делает обрезку видимой в логах/UI.
const truncationMarker = "…(truncated)"

// truncateRunes режет строку до max СИМВОЛОВ (рун), если на узле включён лимит
// max_body_size (§22.2). Режем по рунам, а не по байтам, чтобы не порвать
// многобайтовый UTF-8 и не получить битую запись в ClickHouse. checksum считает
// вызывающая сторона по полному телу ДО обрезки — целостность сохраняется. На
// тело, отдаваемое клиенту, обрезка НЕ влияет (только лог).
func truncateRunes(s string, enabled bool, max int32) string {
	if !enabled || max <= 0 {
		return s
	}
	runes := []rune(s)
	if int32(len(runes)) <= max {
		return s
	}
	return string(runes[:max]) + truncationMarker
}

// appendRedirectNote дописывает к reason лога краткую сводку по редиректам (§50):
// сколько переходов, смена схемы, и предупреждение о потере тела при смене
// метода (301/302/303 POST→GET). URL берутся уже отредаченными из httpclient.
// Пример: "OK · 1 редирект: http→https; тело запроса потеряно (POST→GET) —
// http://x/a → https://x/a".
func appendRedirectNote(reason string, hops []port.RedirectHop) string {
	if len(hops) == 0 {
		return reason
	}
	first, last := hops[0], hops[len(hops)-1]
	bodyDropped := false
	for _, h := range hops {
		if h.FromMethod != h.ToMethod {
			bodyDropped = true
			break
		}
	}
	note := fmt.Sprintf("%d редирект(ов) — %s → %s", len(hops), first.From, last.To)
	if bodyDropped {
		note += "; тело запроса потеряно (POST→GET)"
	}
	if reason == "" {
		return note
	}
	return reason + " · " + note
}

// extractQuery возвращает query-string из URL без ведущего "?".
// Чувствительные значения (токены) предполагается, что уже исключены
// Receiver'ом перед передачей в Sender (§3.5).
func extractQuery(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.RawQuery
}
