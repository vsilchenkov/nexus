package mail

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP — минимальный SMTP-сервер на 127.0.0.1 для offline-тестов (§88.3).
//
// httptest тут неприменим (это не HTTP), а поднимать настоящий релей ради
// проверки заголовков письма несоразмерно. Сервер отвечает ровно столько,
// сколько нужно клиенту для полного цикла EHLO → AUTH → MAIL → RCPT → DATA,
// и складывает полученное тело, чтобы тест мог его разобрать.
type fakeSMTP struct {
	ln net.Listener

	mu       sync.Mutex
	commands []string // все полученные команды, в порядке поступления
	data     string   // тело последнего письма (без завершающей точки)
	authArg  string   // аргумент команды AUTH (base64-полезная нагрузка)

	// failAt и silentGreeting задаются ТОЛЬКО опциями конструктора и после
	// старта не меняются. Раньше тесты писали их полями уже работающему
	// серверу — это была настоящая гонка (нашёл контейнерный -race; нативный
	// прогон на Windows её не увидел).
	failAt         string
	silentGreeting bool
}

// fakeSMTPOption настраивает сервер ДО того, как он начал принимать соединения.
type fakeSMTPOption func(*fakeSMTP)

// withFailAt заставляет сервер ответить 550 на указанную команду.
func withFailAt(verb string) fakeSMTPOption {
	return func(s *fakeSMTP) { s.failAt = verb }
}

// withSilentGreeting — сервер не шлёт приветствие 220 вовсе: клиент обязан
// упереться в таймаут или отмену контекста.
func withSilentGreeting() fakeSMTPOption {
	return func(s *fakeSMTP) { s.silentGreeting = true }
}

func newFakeSMTP(t *testing.T, opts ...fakeSMTPOption) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{ln: ln}
	for _, o := range opts {
		o(s)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serve()
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done // без ожидания goleak увидел бы горутину сервера
	})
	return s
}

func (s *fakeSMTP) addr() (host string, port int) {
	a := s.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (s *fakeSMTP) serve() {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener закрыт в Cleanup
		}
		wg.Go(func() { s.handle(conn) })
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	reply := func(format string, args ...any) bool {
		if _, err := fmt.Fprintf(w, format+"\r\n", args...); err != nil {
			return false
		}
		return w.Flush() == nil
	}

	if s.silentGreeting {
		// Держим соединение открытым и молчим: клиент обязан уйти по таймауту.
		_, _ = r.ReadString('\n')
		return
	}
	if !reply("220 fake.local ESMTP ready") {
		return
	}

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)

		s.mu.Lock()
		s.commands = append(s.commands, line)
		if verb == "AUTH" {
			// «AUTH PLAIN <base64>» либо «AUTH LOGIN»
			_, payload, _ := strings.Cut(arg, " ")
			s.authArg = payload
		}
		s.mu.Unlock()

		if s.failAt != "" && verb == s.failAt {
			if !reply("550 5.7.1 rejected by fake server") {
				return
			}
			continue
		}

		switch verb {
		case "EHLO", "HELO":
			ok := reply("250-fake.local greets you") &&
				reply("250-AUTH PLAIN LOGIN CRAM-MD5") &&
				reply("250-SIZE 10485760") &&
				reply("250 8BITMIME")
			if !ok {
				return
			}
		case "AUTH":
			if !reply("235 2.7.0 authentication succeeded") {
				return
			}
		case "DATA":
			if !reply("354 end data with <CR><LF>.<CR><LF>") {
				return
			}
			body, err := readDotBody(r)
			if err != nil {
				return
			}
			s.mu.Lock()
			s.data = body
			s.mu.Unlock()
			if !reply("250 2.0.0 accepted") {
				return
			}
		case "QUIT":
			reply("221 2.0.0 bye")
			return
		default:
			// MAIL, RCPT, RSET, NOOP и всё прочее.
			if !reply("250 2.0.0 ok") {
				return
			}
		}
	}
}

// readDotBody читает тело письма до строки «.» и снимает dot-stuffing.
// CRLF намеренно СОХРАНЯЮТСЯ: тесты разбирают результат настоящими
// парсерами (net/mail, mime/multipart), а те требуют канонических concов строк.
func readDotBody(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if line == ".\r\n" || line == ".\n" {
			return b.String(), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		b.WriteString(line)
	}
}

func (s *fakeSMTP) received() (commands []string, data, authArg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...), s.data, s.authArg
}
