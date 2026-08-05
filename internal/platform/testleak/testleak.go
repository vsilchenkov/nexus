// Package testleak — общие наборы исключений goleak для TestMain (§79.6,
// политика CLAUDE.md §8).
//
// Зачем пакет, а не копипаста в каждом _test.go: список «чужих» фоновых горутин
// зависит от версий net/http и grpc-go и меняется при обновлении зависимостей.
// Один экземпляр правится в одном месте; двадцать копий расходятся.
//
// Правило приоритета: исключение допустимо ТОЛЬКО для горутин, которые мы не
// можем остановить (пулы соединений чужих библиотек). Свои горутины джойнятся в
// тесте — t.Cleanup, канал завершения, явный Close. Глушить исключением свою
// утечку значит спрятать будущий баг.
//
// Пакет импортируется только из тестов; в продакшн-бинарь не попадает.
package testleak

import (
	"testing"

	"go.uber.org/goleak"
)

// Verify — обёртка над goleak.VerifyTestMain с дополнительными опциями.
//
//	func TestMain(m *testing.M) { testleak.Verify(m, testleak.HTTPClient()...) }
func Verify(m *testing.M, opts ...goleak.Option) {
	goleak.VerifyTestMain(m, opts...)
}

// HTTPClient — фоновые горутины пула соединений net/http.
//
// httptest-сервер закрывается в тесте, но keep-alive соединения КЛИЕНТА живут
// своим циклом (readLoop/writeLoop) ещё десятки секунд после Close, и на момент
// проверки goleak они видны. Ожидание poller'а (internal/poll.runtime_pollWait)
// в стеке не верхнее, поэтому для него нужен IgnoreAnyFunction.
//
// Альтернатива без исключений — Transport.CloseIdleConnections() в t.Cleanup;
// там, где клиент доступен тесту, предпочтителен именно он.
func HTTPClient() []goleak.Option {
	return []goleak.Option{
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"),
	}
}

// GRPC — фон grpc-go поверх набора HTTPClient: keepalive транспорта, чтение
// управляющего буфера, сериализатор колбэков и переподключение адреса. Все они
// живут внутри ClientConn и не завершаются синхронно с Close соединения.
func GRPC() []goleak.Option {
	return append(HTTPClient(),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*controlBuffer).get"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransport"),
	)
}
