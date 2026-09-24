package domain

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// ErrStreamDeclined — §107.2: узел не открывает SSE-поток на GET. Receiver
// отвечает 405 сам, не вызывая получателя. Это решение настройки узла, а не
// отказ шины, поэтому в журнал отказов §94 ответ не попадает. Конкретное
// значение — *StreamDeclinedError (несёт заголовок Allow); сравнивать через
// errors.Is(err, ErrStreamDeclined).
var ErrStreamDeclined = errors.New("domain: event stream is not supported by this node")

// StreamDeclinedError — отказ от SSE-подписки вместе с методами, которые узел
// принимает (RFC 9110 требует заголовок Allow в ответе 405). Пустой Allow —
// узел принимает только GET: подписка отклонена, а другого метода нет.
type StreamDeclinedError struct {
	Allow string
}

func (e *StreamDeclinedError) Error() string { return ErrStreamDeclined.Error() }

// Is делает *StreamDeclinedError равным sentinel'у ErrStreamDeclined.
func (e *StreamDeclinedError) Is(target error) bool { return target == ErrStreamDeclined }

// sseMediaType — медиатип потока событий (WHATWG HTML, Server-Sent Events).
const sseMediaType = "text/event-stream"

// allowForAnyMethod — Allow узла с входящим методом ANY (§40): всё, что
// принимает Receiver, кроме самого GET, на котором подписка и отклонена.
const allowForAnyMethod = "POST, PUT, PATCH, DELETE, HEAD, OPTIONS"

// IsSSESubscribe сообщает, является ли запрос подпиской на поток событий:
// метод GET и в Accept есть text/event-stream с q ≠ 0. Accept разбирается по
// элементам списка; битый элемент пропускается, а не роняет разбор.
//
// POST с тем же Accept подпиской не считается: MCP-клиент шлёт так вызовы
// инструментов, и сервер вправе ответить коротким SSE — у такого ответа есть
// конец, и шина отдаёт его целиком.
func IsSSESubscribe(method string, h http.Header) bool {
	if !strings.EqualFold(method, http.MethodGet) {
		return false
	}
	for _, value := range h.Values("Accept") {
		for item := range strings.SplitSeq(value, ",") {
			if acceptsEventStream(item) {
				return true
			}
		}
	}
	return false
}

// acceptsEventStream — один элемент Accept: text/event-stream без q=0.
func acceptsEventStream(item string) bool {
	mediaType, params, err := mime.ParseMediaType(item)
	if err != nil || mediaType != sseMediaType {
		return false
	}
	q, ok := params["q"]
	if !ok {
		return true
	}
	// q=0 — «не присылать этот тип» (RFC 9110 §12.4.2). Нечитаемый q считаем
	// обычным весом: клиент явно назвал поток событий.
	w, err := strconv.ParseFloat(q, 64)
	return err != nil || w > 0
}

// DeclineStream — §107.2: nil, если запрос идёт дальше; *StreamDeclinedError,
// если узел с включённым SSEDecline получил подписку на поток событий.
// Действует только там, где флаг применим (sseDeclineApplies): у остальных
// узлов его сбрасывает NormalizeForRootMethod, а проверка здесь страхует от
// ручной правки БД.
func (n *Node) DeclineStream(method string, h http.Header) error {
	if !n.SSEDecline || !n.sseDeclineApplies() || !IsSSESubscribe(method, h) {
		return nil
	}
	return &StreamDeclinedError{Allow: allowWithoutGET(n.IncomingMethod)}
}

// sseDeclineApplies — флаг §107.2 осмыслен: sync-узел, принимающий GET.
// async-узел отвечает клиенту сам, не дожидаясь получателя, — подписка не
// висит на входе; у pull-узла входа нет вовсе. Узел с другим входящим методом
// (POST, PUT, …; пустой = POST) отвечает на GET отказом §40 и без флага —
// скрытый в форме флаг подменял бы этот отказ молча.
func (n *Node) sseDeclineApplies() bool {
	return n.RootMethod == RootMethodRequest &&
		(n.IncomingMethod == HTTPMethodAny || n.IncomingMethod == HTTPMethodGET)
}

// allowWithoutGET — методы узла для заголовка Allow, кроме GET. Флаг действует
// только при входящем ANY или GET (sseDeclineApplies), поэтому других веток
// нет: узел, принимающий лишь GET, другого метода не предлагает — Allow пустой.
func allowWithoutGET(incoming HTTPMethod) string {
	if incoming == HTTPMethodAny {
		return allowForAnyMethod
	}
	return ""
}
