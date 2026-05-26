package port

import (
	"context"
	"net/url"
)

// DispatchRequest — что отправляем через шину для replay (§7.4.1 ТЗ).
type DispatchRequest struct {
	NodePath string     // {path} в /v1/request/{path}
	Async    bool       // true → /v1/requestAsync
	Method   string     // POST / GET / ...
	Query    url.Values // итоговый query (включая служебный __replay_of=<id>)
	Headers  map[string]string
	Body     []byte
}

// DispatchResponse — ответ шины (для sync — реальный ответ внешнего узла,
// для async — {id, queued?}).
type DispatchResponse struct {
	StatusCode int
	Body       []byte
	Headers    map[string]string
}

// ReceiverDispatcher отправляет HTTP-запрос обратно в Receiver Service для
// replay. Это сознательное прохождение через реальный pipeline шины —
// нет smart-bypass'ов (§7.4.1).
type ReceiverDispatcher interface {
	Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResponse, error)
}
