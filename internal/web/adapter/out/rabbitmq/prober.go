// Package rabbitmq — адаптеры поверх github.com/rabbitmq/amqp091-go для Web
// Service. Здесь — Prober: диагностический AMQP-handshake для
// POST /api/nodes/test-rmq (§27.8). Не забирает сообщения и не меняет
// состояние очереди (queue.declare с passive=true).
package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"nexus/internal/web/usecase"
)

// Prober реализует usecase.RMQDialer. dialTimeout — таймаут одного шага.
type Prober struct {
	dialTimeout time.Duration
}

var _ usecase.RMQDialer = (*Prober)(nil)

func NewProber() *Prober {
	return &Prober{dialTimeout: 5 * time.Second}
}

// Probe выполняет три шага: TCP-connect, AMQP-handshake+auth, passive
// queue.declare. Каждый шаг — со своим elapsed_ms; первый провал
// останавливает цепочку (последующие шаги остаются OK=false без времени).
func (p *Prober) Probe(ctx context.Context, in usecase.RMQProbeParams) usecase.RMQTestResult {
	var res usecase.RMQTestResult

	// Шаг 1: TCP-connect (без AMQP) — отделяет «сеть/хост недоступны» от
	// «не та авторизация».
	addr := net.JoinHostPort(in.Host, fmt.Sprintf("%d", in.Port))
	start := time.Now()
	d := net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	res.Checks.Connect.ElapsedMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Checks.Connect.Error = fmt.Sprintf("не удалось подключиться к %s: %v", addr, err)
		return res
	}
	_ = conn.Close()
	res.Checks.Connect.OK = true

	// Шаг 2: полный AMQP-handshake (включая PLAIN-auth и vhost).
	amqpConn, err := p.dial(in)
	res.Checks.Auth.ElapsedMs = time.Since(start).Milliseconds() - res.Checks.Connect.ElapsedMs
	if err != nil {
		res.Checks.Auth.Error = authErrorText(err)
		return res
	}
	defer amqpConn.Close()
	res.Checks.Auth.OK = true

	// Шаг 3: passive queue.declare — проверяет существование очереди, не
	// создавая её и не получая прав на запись.
	ch, err := amqpConn.Channel()
	if err != nil {
		res.Checks.Queue.Error = fmt.Sprintf("не удалось открыть канал: %v", err)
		return res
	}
	defer ch.Close()

	qStart := time.Now()
	q, err := ch.QueueDeclarePassive(in.Queue, true, false, false, false, nil)
	res.Checks.Queue.ElapsedMs = time.Since(qStart).Milliseconds()
	if err != nil {
		res.Checks.Queue.Error = queueErrorText(in.Queue, err)
		return res
	}
	res.Checks.Queue.OK = true
	res.Checks.Queue.MessageCount = q.Messages
	res.Checks.Queue.ConsumerCount = q.Consumers
	res.OK = true
	return res
}

// dial открывает amqp.Connection с нужной схемой (amqp/amqps) и таймаутом.
func (p *Prober) dial(in usecase.RMQProbeParams) (*amqp.Connection, error) {
	scheme := "amqp"
	if in.UseTLS {
		scheme = "amqps"
	}
	uri := amqp.URI{
		Scheme:   scheme,
		Host:     in.Host,
		Port:     in.Port,
		Username: in.User,
		Password: in.Password,
		Vhost:    in.VHost,
	}
	cfg := amqp.Config{
		Dial: amqp.DefaultDial(p.dialTimeout),
	}
	if in.UseTLS {
		cfg.TLSClientConfig = &tls.Config{ServerName: in.Host, MinVersion: tls.VersionTLS12}
	}
	return amqp.DialConfig(uri.String(), cfg)
}

// authErrorText переводит ошибку handshake в понятную причину.
func authErrorText(err error) string {
	var ae *amqp.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case amqp.AccessRefused: // 403
			return "доступ запрещён: проверьте логин/пароль или права на vhost"
		case amqp.NotAllowed:
			return fmt.Sprintf("vhost недоступен: %s", ae.Reason)
		}
		return ae.Reason
	}
	return fmt.Sprintf("ошибка AMQP-handshake: %v", err)
}

// queueErrorText переводит ошибку passive-declare (частый кейс — опечатка в
// имени очереди → 404 NOT_FOUND).
func queueErrorText(queue string, err error) string {
	var ae *amqp.Error
	if errors.As(err, &ae) && ae.Code == amqp.NotFound { // 404
		return fmt.Sprintf("очередь %q не найдена (404 NOT_FOUND). Проверьте имя или создайте очередь и привяжите к exchange.", queue)
	}
	return fmt.Sprintf("ошибка проверки очереди %q: %v", queue, err)
}
