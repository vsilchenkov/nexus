package usecase

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	rcv "nexus/internal/receiver/usecase"
	"nexus/internal/web/usecase/port"
	senderv1 "nexus/proto/sender/v1"
)

// DryRunRequest — что приходит в POST /api/nodes/dry-run.
//
// Node — полная (несохранённая) конфигурация узла из формы. Request —
// синтетический запрос, который пользователь хочет «прогнать» через
// шину. UseMock=true (по умолчанию) — реальный HTTP-вызов внешнего
// узла НЕ выполняется; Sender замещается фиктивным 200-ответом.
type DryRunRequest struct {
	Node   *domain.Node
	Method string
	// PathTail — хвост входящего пути после пути узла (§39 path-passthrough).
	// Боевой Receiver приклеивает его к target URL; без него тест
	// passthrough-узла бил бы в базовый адрес, а не в реальный (§55.9).
	PathTail string
	Query    url.Values
	Headers  http.Header
	Body     []byte
	UseMock  bool
}

// DryRunStep — один шаг отчёта (§7.5.1 ТЗ).
type DryRunStep struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // ok | failed | skipped
	Message string `json:"message,omitempty"`
	Detail  any    `json:"detail,omitempty"`
}

// DryRunReport — структурированный пошаговый отчёт о прогоне.
type DryRunReport struct {
	ID    string       `json:"id"`
	Steps []DryRunStep `json:"steps"`
	OK    bool         `json:"ok"`
}

// DryRunUsecase — реализует §7.5.1 (тестовый запрос) и §55 (реальный вызов).
type DryRunUsecase struct {
	audit *AuditUsecase
	// sender — вызов target'а в реальном режиме (§55). nil → реальный режим
	// недоступен (шаг response = skipped), mock работает как прежде: Web не
	// обязан знать про Sender ради базового сценария.
	sender port.SenderClient
	// selfIngressHosts — свои authority для проверки §32.2: реальный вызов не
	// должен бить в собственный ingress шины (петля). Пусто → проверка
	// пропускается (как в NodeUsecase).
	selfIngressHosts []string
	logger           logging.Logger
}

func NewDryRunUsecase(
	audit *AuditUsecase,
	sender port.SenderClient,
	selfIngressHosts []string,
	logger logging.Logger,
) *DryRunUsecase {
	return &DryRunUsecase{audit: audit, sender: sender, selfIngressHosts: selfIngressHosts, logger: logger}
}

// Run выполняет шаги pipeline'а Receiver на копии конфига узла из формы
// и возвращает структурированный отчёт. Никаких побочных эффектов кроме
// записи в audit.
func (u *DryRunUsecase) Run(ctx context.Context, actor Actor, req DryRunRequest) (*DryRunReport, error) {
	if req.Node == nil {
		return nil, errors.New("dry-run: node is required")
	}
	req.Node.SetDefaults()
	if err := req.Node.Validate(); err != nil {
		return nil, err
	}

	rep := &DryRunReport{ID: uuid.NewString(), OK: true}

	// 0. §40: метод. Боевой Receiver сперва проверяет, принимает ли узел такой
	// входящий метод, и только потом вычисляет исходящий (ANY = зеркалить
	// входящий, иначе — фиксированный метод узла). Без этих двух шагов dry-run
	// врал: узел с incoming_method=GET «принимал» POST, а узел с
	// outgoing_method=PUT получал в тесте POST вместо PUT (§55.9).
	if !rcv.MethodMatches(req.Method, req.Node.IncomingMethod) {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "method.incoming", Status: "failed",
			Message: fmt.Sprintf("node accepts %s, got %s", req.Node.IncomingMethod, req.Method),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:method.incoming")
		return rep, nil
	}
	outMethod := rcv.EffectiveOutgoingMethod(req.Node, req.Method)
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "method.incoming", Status: "ok",
		Message: fmt.Sprintf("%s → %s", req.Method, outMethod),
		Detail: map[string]any{
			"incoming":          req.Method,
			"outgoing":          outMethod,
			"node_incoming":     string(req.Node.IncomingMethod),
			"node_outgoing":     string(req.Node.OutgoingMethod),
			"outgoing_mirrored": req.Node.OutgoingMethod == domain.HTTPMethodAny,
		},
	})

	// 1. Incoming auth. Для узла с заведёнными кредами сервер сам подставляет
	// ожидаемую входящую креду в синтетический запрос (наружу креды не отдаются,
	// собрать `Basic base64(...)`/HMAC руками оператор не может, §55.6). Ручной
	// ввод оператора побеждает — тогда autofill не срабатывает и негативный
	// сценарий §55.8 (клиент шлёт креду не туда) остаётся проверяемым.
	authPres, authFilled := u.autofillIncomingAuth(req.Node, req.Headers, req.Query, req.Body)
	if err := rcv.CheckIncomingAuth(req.Node, req.Headers, req.Query, req.Body); err != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "auth.incoming", Status: "failed", Message: err.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:auth.incoming")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	incomingDetail := map[string]any{
		"type":       string(req.Node.IncomingAuthType),
		"autofilled": authFilled, // подставил ли сервер креду из узла (§55.6)
	}
	if authFilled {
		incomingDetail["source"] = string(authPres.Source)
		incomingDetail["field"] = authPres.Field // только имя поля, без значения
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "auth.incoming", Status: "ok",
		Message: fmt.Sprintf("type=%s", req.Node.IncomingAuthType),
		Detail:  incomingDetail,
	})

	// 2. Outgoing auth + cleanup query/headers/body.
	effHeaders := cloneHeader(req.Headers)
	effQuery := cloneValues(req.Query)
	effBody := req.Body
	var authHeader string
	var authErr error
	if req.Node.AuthType.IsDynamic() {
		dyn, derr := rcv.BuildDynamicOutgoingAuth(req.Node, req.Headers, req.Query, req.Body)
		if derr != nil {
			authErr = derr
		} else {
			authHeader = dyn.Header
			effHeaders = dyn.Headers
			effQuery = dyn.Query
			effBody = dyn.Body
		}
	} else {
		authHeader, authErr = rcv.BuildOutgoingAuth(req.Node)
	}
	if authErr != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "auth.outgoing", Status: "failed", Message: authErr.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:auth.outgoing")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name:    "auth.outgoing",
		Status:  "ok",
		Message: maskAuthHeader(authHeader),
		Detail: map[string]any{
			"type":   string(req.Node.AuthType),
			"length": len(authHeader),
		},
	})

	// 3. URL resolve.
	target, cleanQuery, urlErr := rcv.ResolveURL(req.Node, effQuery)
	if urlErr != nil {
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "url.resolve", Status: "failed", Message: urlErr.Error(),
		})
		rep.OK = false
		u.writeAudit(ctx, actor, req.Node, rep, "failed:url.resolve")
		return rep, nil //nolint:nilerr // dry-run: ошибка узла — это результат, а не сбой операции
	}
	// §39: хвост входящего пути приклеивается к резолвнутому адресу — тем же
	// кодом, что в боевом Receiver (JoinPath режет «../», клиент не выйдет за
	// пределы target_url).
	tail := req.PathTail
	if !req.Node.PathPassthrough {
		// Узел без passthrough хвост игнорирует — показываем это явно, иначе
		// оператор решит, что тест «съел» его ввод.
		tail = ""
	}
	target = rcv.AppendPathSuffix(target, tail)
	finalURL := appendQueryToURL(target, cleanQuery)
	// displayURL — URL для отчёта: если входящая креда автоподставлена в query
	// (§55.6), её значение в URL маскируется. В реальный вызов уходит finalURL.
	displayURL := finalURL
	if authFilled && authPres.Source == domain.IncomingAuthSourceQuery {
		displayURL = maskQueryParam(finalURL, authPres.Field)
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "url.resolve", Status: "ok", Message: displayURL,
		Detail: map[string]any{
			"mode":              string(req.Node.URLMode),
			"allowlist_passed":  true,
			"path_passthrough":  req.Node.PathPassthrough,
			"path_tail":         tail,
			"path_tail_ignored": req.PathTail != "" && !req.Node.PathPassthrough,
		},
	})

	// 4. Headers forwarded. fwd — реальные заголовки для Sender'а; reportFwd —
	// копия для отчёта, где автоподставленная входящая креда (если узел форвардит
	// своё auth-поле) маскируется, чтобы сохранённый секрет не утёк в браузер.
	fwd := map[string]string{}
	for _, name := range req.Node.ForwardHeaders {
		if v := effHeaders.Get(name); v != "" {
			fwd[http.CanonicalHeaderKey(name)] = v
		}
	}
	reportFwd := fwd
	if authFilled && authPres.Source == domain.IncomingAuthSourceHeader {
		reportFwd = make(map[string]string, len(fwd))
		for k, v := range fwd {
			if strings.EqualFold(k, authPres.Field) {
				reportFwd[k] = maskAuthHeader(v)
				continue
			}
			reportFwd[k] = v
		}
	}
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "headers.forwarded", Status: "ok", Detail: reportFwd,
	})

	// 5. Body sent.
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "body.sent", Status: "ok",
		Detail: map[string]any{
			"size_bytes": len(effBody),
			"preview":    bodyPreview(effBody, 256),
		},
	})

	// 6. Response: mock (по умолчанию) или реальный вызов target через Sender (§55).
	if req.UseMock {
		// §7.5.1: mock отвечает 200 со случайной задержкой 50–100 мс. Живёт в
		// Web, а не в Sender: gRPC-хоп ради синтетики не нужен, и зависимость
		// Web→Sender остаётся опциональной (см. §55.8).
		mockLatency := 50 + rand.Intn(51) //nolint:gosec // не крипто: имитация задержки
		rep.Steps = append(rep.Steps, DryRunStep{
			Name: "response", Status: "ok",
			Detail: map[string]any{
				"status":      200,
				"duration_ms": mockLatency,
				"body":        `{"mock":true}`,
				"source":      "mock-server",
			},
		})
	} else {
		step := u.realCall(ctx, req, outMethod, finalURL, authHeader, fwd, effBody)
		rep.Steps = append(rep.Steps, step)
		if step.Status == "failed" {
			rep.OK = false
			u.writeAudit(ctx, actor, req.Node, rep, "failed:response")
			return rep, nil
		}
	}

	// 7. Would log to ClickHouse.
	rep.Steps = append(rep.Steps, DryRunStep{
		Name: "clickhouse.would_log", Status: "ok",
		Detail: map[string]any{
			"table": req.Node.ClickHouseTable,
			"id":    rep.ID,
			"url":   displayURL,
			// Метод исходящего вызова (§40), а не метод формы: в логах узла
			// пишется именно он.
			"method": outMethod,
		},
	})

	u.writeAudit(ctx, actor, req.Node, rep, "ok")
	return rep, nil
}

// dryRunPathPrefix — префикс временного пути для реального вызова (§55.4).
// Sender гасит побочку по флагу dry_run, но node_path всё равно подменяем: если
// какой-то гейт будет забыт при будущих правках, метрика/breaker/Redis-ключ
// уйдут в изолированное имя, а не на боевой узел.
const dryRunPathPrefix = "__dryrun_"

// realCall — шаг «Response» в реальном режиме: вызов target через Sender (§55.3).
// Идёт тем же путём и тем же HTTP-клиентом, что боевой трафик, поэтому
// показывает настоящие таймауты, TLS-ошибки и редиректы — ради этого §55.
// outMethod — исходящий метод, уже пересчитанный по §40 (может отличаться от
// метода формы: узел с фиксированным outgoing_method переопределяет входящий).
func (u *DryRunUsecase) realCall(
	ctx context.Context,
	req DryRunRequest,
	outMethod string,
	finalURL, authHeader string,
	fwd map[string]string,
	body []byte,
) DryRunStep {
	if u.sender == nil {
		return DryRunStep{
			Name: "response", Status: "skipped",
			Message: "real call is not available: web.sender_grpc.addr is not configured",
		}
	}
	// §32.2: реальный вызов не должен бить в собственный ingress шины — это
	// петля через самих себя. При mock проверка не нужна (запрос никуда не
	// уходит), поэтому она здесь, а не в общей части. Та же функция, что у
	// NodeUsecase при сохранении узла, — но по РЕЗОЛВНУТОМУ URL (у from_request
	// статического target нет, а реальный адрес известен только сейчас).
	if len(u.selfIngressHosts) > 0 && isSelfReferenceTarget(finalURL, u.selfIngressHosts) {
		return DryRunStep{
			Name: "response", Status: "failed",
			Message: domain.ErrNodeTargetURLSelfReference.Error(),
		}
	}

	headers := make(map[string]string, len(fwd))
	for k, v := range fwd {
		headers[k] = v
	}
	grpcReq := &senderv1.SendRequest{
		Id: uuid.NewString(),
		// Изолированное имя вместо реального пути — см. dryRunPathPrefix.
		NodePath:  dryRunPathPrefix + req.Node.Path,
		TargetUrl: finalURL,
		Method:    outMethod,
		Headers:   headers,
		Body:      body,
		TimeoutMs: req.Node.TimeoutMs,
		// Ретраи узла воспроизводим: оператор должен видеть то же поведение,
		// что у боевого трафика (в т.ч. сколько раз шина пыталась достучаться).
		RetryCount:     req.Node.RetryCount,
		RetryBackoffMs: req.Node.RetryBackoffMs,
		// §55.4: три гейта. logging_enabled=false + dry_run=true → ни записи в
		// ClickHouse, ни метрик/статуса узла, ни участия в circuit breaker.
		LoggingEnabled: false,
		DryRun:         true,
	}
	if authHeader != "" {
		grpcReq.Auth = &senderv1.AuthConfig{AuthorizationHeader: authHeader}
	}

	// Длительность берём из ответа Sender'а (он мерит сам вызов, без gRPC-хопа) —
	// это то же число, что видит боевой трафик в логах.
	resp, err := u.sender.Send(ctx, grpcReq)
	if err != nil {
		// Сбой самого gRPC (Sender недоступен) — это не «узел не ответил»,
		// а отказ инструмента: различаем в сообщении.
		u.logger.Debug("dry-run: sender call failed",
			u.logger.Str("url", redactQuery(finalURL)), u.logger.Err(err))
		return DryRunStep{
			Name: "response", Status: "failed",
			Message: "sender is unavailable: " + err.Error(),
		}
	}

	detail := map[string]any{
		"status":      resp.GetStatusCode(),
		"duration_ms": resp.GetDurationMs(),
		"attempts":    resp.GetAttempts(),
		"body":        bodyPreview(resp.GetBody(), 256),
		"headers":     maskAuthMap(resp.GetHeaders()),
		"source":      "sender",
	}
	if e := resp.GetError(); e != "" {
		detail["error"] = e
	}
	u.logger.Debug("dry-run: real call finished",
		u.logger.Str("url", redactQuery(finalURL)),
		u.logger.Int("status", int(resp.GetStatusCode())),
		u.logger.Int("duration_ms", int(resp.GetDurationMs())),
		u.logger.Int("attempts", int(resp.GetAttempts())))

	// Узел не ответил или ответил ошибкой транспорта — это результат теста
	// (failed), а не сбой операции. HTTP-статус 4xx/5xx считаем успешным
	// шагом: ответ получен, оператор видит код и решает сам.
	if resp.GetStatusCode() == 0 {
		return DryRunStep{
			Name: "response", Status: "failed",
			Message: firstNonEmpty(resp.GetError(), "no response from target"),
			Detail:  detail,
		}
	}
	return DryRunStep{Name: "response", Status: "ok", Detail: detail}
}

// autofillIncomingAuth подставляет в синтетический запрос (h/q) ожидаемую входящую
// креду, построенную сервером из сохранённых кредов узла (§55.6). Возвращает
// (презентация, true), если подстановка выполнена. Ручной ввод оператора
// побеждает: если предъявленное значение уже непусто — не трогаем (false), и
// негативный сценарий §55.8 остаётся проверяемым. Значение креды в лог не пишем.
func (u *DryRunUsecase) autofillIncomingAuth(node *domain.Node, h http.Header, q url.Values, body []byte) (rcv.IncomingAuthPresentation, bool) {
	if rcv.IncomingAuthPresented(node, h, q) != "" {
		return rcv.IncomingAuthPresentation{}, false
	}
	pres, ok, err := rcv.BuildIncomingAuthValue(node, body)
	if err != nil || !ok {
		return rcv.IncomingAuthPresentation{}, false
	}
	if pres.Source == domain.IncomingAuthSourceQuery {
		q.Set(pres.Field, pres.Value)
	} else {
		h.Set(pres.Field, pres.Value)
	}
	u.logger.Debug("dry-run: incoming auth autofilled from node",
		u.logger.Str("type", string(node.IncomingAuthType)),
		u.logger.Str("source", string(pres.Source)),
		u.logger.Str("field", pres.Field))
	return pres, true
}

// maskQueryParam возвращает URL с замаскированным значением параметра param
// (`***`). Нужен, чтобы автоподставленная входящая креда из query-источника не
// утекла открытым текстом в отчёт (в реальный вызов уходит настоящий URL).
func maskQueryParam(raw, param string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := parsed.Query()
	if !q.Has(param) {
		return raw
	}
	q.Set(param, "***")
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// maskAuthMap — копия заголовков ответа с маскированной авторизацией: эхо
// внешнего узла не должно светить креды в отчёте.
func maskAuthMap(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if strings.EqualFold(k, "Authorization") {
			out[k] = maskAuthHeader(v)
			continue
		}
		out[k] = v
	}
	return out
}

// redactQuery — URL без query для логов: там живут токены (§50 redactURL).
func redactQuery(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (u *DryRunUsecase) writeAudit(ctx context.Context, actor Actor, n *domain.Node, rep *DryRunReport, outcome string) {
	u.audit.Log(ctx, actor, domain.ActionNodeDryRun, "node", n.ID, map[string]any{
		"path":    n.Path,
		"dry_run": rep.ID,
		"outcome": outcome,
	})
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vv := range v {
		out[k] = append([]string(nil), vv...)
	}
	return out
}

func appendQueryToURL(target string, q url.Values) string {
	if len(q) == 0 {
		return target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	existing := parsed.Query()
	for k, vv := range q {
		for _, v := range vv {
			existing.Add(k, v)
		}
	}
	parsed.RawQuery = existing.Encode()
	return parsed.String()
}

// maskAuthHeader заменяет всё, что после "Basic "/"Bearer ", на ***.
// Используется для лога/отчётов — реальное значение не отдаётся клиенту.
func maskAuthHeader(h string) string {
	if h == "" {
		return ""
	}
	for _, prefix := range []string{"Basic ", "Bearer "} {
		if strings.HasPrefix(h, prefix) {
			return prefix + "***"
		}
	}
	return "***"
}

func bodyPreview(b []byte, max int) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
