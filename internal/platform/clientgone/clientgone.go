// Package clientgone отличает запрос, который оборвал сам клиент, от сбоя
// сервера (§109.4).
//
// Клиент закрыл соединение или вкладку — net/http отменяет контекст запроса,
// и всё, что под ним выполнялось (запрос в PostgreSQL, вызов Sender'а),
// возвращает отмену. Это не ошибка шины: в Sentry ей делать нечего, а в
// журнале отказов §94 — тем более.
package clientgone

import (
	"context"
	"errors"
)

// Is сообщает, что err — следствие ухода клиента: контекст входящего запроса
// reqCtx ОТМЕНЁН (не истёк по сроку) и сама ошибка — отмена.
//
// Отмена при живом контексте запроса (внутренний таймаут, остановка сервиса)
// уходом клиента не считается и остаётся ошибкой. gRPC-код Canceled сюда не
// доходит в сыром виде: клиент Sender'а переводит его в context.Canceled
// (platform/grpcsender).
func Is(reqCtx context.Context, err error) bool {
	return err != nil &&
		errors.Is(reqCtx.Err(), context.Canceled) &&
		errors.Is(err, context.Canceled)
}
