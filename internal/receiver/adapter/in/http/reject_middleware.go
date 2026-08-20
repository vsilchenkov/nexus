package http

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"nexus/internal/domain"
	"nexus/internal/platform/requestid"
	"nexus/internal/receiver/adapter/out/rejectlog"
)

// RejectReasonKey — ключ gin-контекста, под которым обработчик кладёт код
// причины отказа (§94.2).
//
// Без него 404 «узла нет», 404 «async-эндпоинт на sync-узле» (§82.3) и 404
// «pull-узел, входящего HTTP нет» слились бы в одну строку журнала: наружу они
// намеренно неотличимы, и по статусу их не разделить.
const RejectReasonKey = "nexus_reject_reason"

// SetRejectReason проставляет причину отказа для журнала (§94).
func SetRejectReason(c *gin.Context, r domain.RejectReason) {
	c.Set(RejectReasonKey, string(r))
}

// RejectSink — приёмник записей журнала. Интерфейс объявлен на стороне
// потребителя: middleware не знает ни про PostgreSQL, ни про агрегацию.
//
// Экспортирован ради проводки в app: выключенный журнал обязан приезжать сюда
// как nil ИНТЕРФЕЙС, а для этого вызывающему нужен сам тип (nil-указатель
// внутри интерфейса даёт не-nil интерфейс).
type RejectSink interface {
	Add(s rejectlog.Sample)
}

// rejectMetrics — счётчик отказов (реализуется platform/metrics). Отдельно от
// журнала: метрика обязана считать отказы и тогда, когда журнал выключен
// настройкой — иначе выключение сбора делает шину слепой целиком.
type rejectMetrics interface {
	IncIngressRejected(reason, status, team string)
}

// rejectHeaderWhitelist — заголовки, попадающие в сэмпл (§94.9).
//
// Именно белый список, а не «всё, кроме секретных»: клиент присылает
// произвольные заголовки, и запрет по списку рано или поздно пропустит
// очередной X-Api-Key. Authorization здесь нет — вместо значения сохраняется
// только схема (см. authScheme).
var rejectHeaderWhitelist = []string{
	"User-Agent",
	"Content-Type",
	"Content-Length",
	"X-Forwarded-For",
	"X-Real-Ip",
	requestid.HeaderKey,
}

// RejectLogMiddleware записывает в журнал запросы, которые шина не приняла
// (§94.4).
//
// Стоит после c.Next() ОДНОЙ точкой на всю группу /api/v1, а не в местах
// возврата ошибок: 413 (чтение тела) и 429 (лимит) до доменного слоя не
// доходят вовсе, и разложенная по обработчикам запись их бы не увидела.
//
// sink и m могут быть nil по отдельности: без sink остаётся только метрика,
// без метрики — только журнал; без обоих middleware вырождается в c.Next().
func RejectLogMiddleware(sink RejectSink, m rejectMetrics) gin.HandlerFunc {
	if sink == nil && m == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		c.Next()

		reason, ok := rejectReason(c)
		if !ok {
			return
		}
		status := c.Writer.Status()
		teamSlug, nodePath := rejectTarget(c)
		if teamSlug == "" {
			// Короткая форма §78.1 (один сегмент) — тот же default, каким её
			// видит маршрутизация; домен нормализует так же.
			teamSlug = domain.DefaultTeamSlug
		}
		if m != nil {
			m.IncIngressRejected(string(reason), statusLabel(status), teamSlug)
		}
		if sink == nil {
			return
		}
		sink.Add(rejectlog.Sample{
			Key: domain.RejectedGroupKey{
				TeamSlug:   teamSlug,
				NodePath:   nodePath,
				Reason:     reason,
				HTTPMethod: c.Request.Method,
			},
			Status:    int32(status),
			ClientIP:  clientIP(c.Request),
			UserAgent: domain.TruncateUserAgent(c.Request.UserAgent()),
			RawPath:   rejectRawPath(c.Request.URL),
			BodyBytes: max(c.Request.ContentLength, 0),
			RequestID: requestid.FromContext(c.Request.Context()),
			Headers:   rejectHeaders(c.Request),
		})
	}
}

// rejectReason решает, считается ли ответ отказом, и какой у него код причины.
//
// Приоритет у причины из контекста: обработчик знает, ПОЧЕМУ отказал, а
// статус этого не сохраняет. Без причины пишутся только 4xx и 508 — 5xx это
// сбой шины, его место в Sentry и служебном логе, а не в журнале клиентов
// (единственное исключение — 503 «узел выключен», и там причину ставит
// обработчик).
func rejectReason(c *gin.Context) (domain.RejectReason, bool) {
	if v := c.GetString(RejectReasonKey); v != "" {
		r := domain.RejectReason(v)
		if !r.Valid() {
			r = domain.RejectReasonOther
		}
		return r, true
	}
	status := c.Writer.Status()
	if (status >= 400 && status < 500) || status == http.StatusLoopDetected {
		return domain.RejectReasonForStatus(status), true
	}
	return "", false
}

// rejectTarget разбирает адрес запроса на слог команды и путь узла.
//
// Разбирается именно URL, а не метка узла из контекста: при 404 узла нет и
// канонического пути не существует, а метка к моменту записи уже заменена на
// маркер «не разрезолвлен» (иначе мусорные пути плодили бы ряды Prometheus).
// Для узлов с path_passthrough (§39) в пути остаётся хвост запроса — это
// осознанно: видно, какой именно подпуть дёргают.
func rejectTarget(c *gin.Context) (teamSlug, nodePath string) {
	_, rest := SplitVerb(c.Param("path"))
	return splitTeamSlugAndPath(rest)
}

// rejectRawPath собирает путь запроса с ЗАМАСКИРОВАННЫМИ значениями query.
//
// Имена параметров сохраняются (по ним видно, что клиент вообще прислал),
// значения — нет: там живут токены авторизации (§41, source=query) и
// пользовательские данные, которым в журнале не место.
func rejectRawPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	path := u.Path
	if u.RawQuery == "" {
		return domain.TruncateRawPath(path)
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	// Порядок ключей у url.Values не определён, а путь входит в сэмпл, который
	// читает человек: без сортировки один и тот же запрос выглядел бы каждый
	// раз по-новому.
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte('?')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteString("=***")
	}
	return domain.TruncateRawPath(b.String())
}

// rejectHeaders выбирает заголовки для сэмпла (§94.9).
func rejectHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	for _, name := range rejectHeaderWhitelist {
		if v := r.Header.Get(name); v != "" {
			out[name] = domain.TruncateUserAgent(v)
		}
	}
	// Значение авторизации не сохраняется никогда — только схема: по ней видно,
	// «клиент вообще не представился» или «представился не тем».
	if scheme := authScheme(r.Header.Get("Authorization")); scheme != "" {
		out["Authorization"] = scheme + " ***"
	}
	if r.Host != "" {
		out["Host"] = r.Host
	}
	return out
}

// authScheme возвращает схему заголовка Authorization ("Bearer", "Basic", …)
// без самого секрета. Заголовок без пробела — схемы нет, значит клиент прислал
// голый токен; такой случай обозначается как "raw".
func authScheme(v string) string {
	if v == "" {
		return ""
	}
	scheme, _, ok := strings.Cut(v, " ")
	if !ok || scheme == "" {
		return "raw"
	}
	return scheme
}

// statusLabel — HTTP-код как метка Prometheus.
func statusLabel(status int) string { return strconv.Itoa(status) }
