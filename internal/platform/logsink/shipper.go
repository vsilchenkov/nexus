package logsink

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Дефолты шиппера (§51.3 ТЗ): батч ~64 записей или тик ~1 c, кольцо в Redis —
// 2000 строк на сервис с TTL 1 ч (логи мёртвого сервиса истекают).
const (
	DefaultBatchSize     = 64
	DefaultFlushInterval = time.Second
	DefaultKeyCap        = 2000
	DefaultTTL           = time.Hour
	// finalFlushTimeout — бюджет на дослачу хвоста при останове.
	finalFlushTimeout = 2 * time.Second
	// errLogEvery — каждая какая по счёту ошибка писателя попадает в stderr
	// (первая — всегда). Ошибки НЕ логируются через slog: цепочка снова
	// привела бы записи в этот же шиппер.
	errLogEvery = 100
)

// BatchWriter — порт доставки батча строк (consumer-side; реализация —
// RedisWriter). capN — размер кольца в хранилище, ttl — срок жизни ключа.
type BatchWriter interface {
	Ship(ctx context.Context, key string, lines [][]byte, capN int64, ttl time.Duration) error
}

// ShipperOption настраивает Shipper при создании.
type ShipperOption func(*Shipper)

// WithBatchSize задаёт размер батча, по достижении которого происходит флаш.
func WithBatchSize(n int) ShipperOption {
	return func(s *Shipper) {
		if n > 0 {
			s.batchSize = n
		}
	}
}

// WithFlushInterval задаёт период принудительного флаша неполного батча.
func WithFlushInterval(d time.Duration) ShipperOption {
	return func(s *Shipper) {
		if d > 0 {
			s.flushEvery = d
		}
	}
}

// WithKeyCap задаёт размер кольца в хранилище (LTRIM).
func WithKeyCap(n int64) ShipperOption {
	return func(s *Shipper) {
		if n > 0 {
			s.keyCap = n
		}
	}
}

// WithTTL задаёт срок жизни ключа (EXPIRE).
func WithTTL(d time.Duration) ShipperOption {
	return func(s *Shipper) {
		if d > 0 {
			s.ttl = d
		}
	}
}

// Shipper — фоновый потребитель канала RingHandler'а: копит записи в батч и
// отправляет их писателю. Ошибки писателя не блокируют лог-путь: батч
// выбрасывается, счётчик растёт, редкая диагностика уходит в stderr напрямую.
type Shipper struct {
	writer BatchWriter
	src    <-chan Entry
	key    string

	batchSize  int
	flushEvery time.Duration
	keyCap     int64
	ttl        time.Duration

	shipErrs atomic.Uint64
}

// NewShipper создаёт шиппер для сервиса service, читающий записи из src.
func NewShipper(writer BatchWriter, src <-chan Entry, service string, opts ...ShipperOption) *Shipper {
	s := &Shipper{
		writer:     writer,
		src:        src,
		key:        Key(service),
		batchSize:  DefaultBatchSize,
		flushEvery: DefaultFlushInterval,
		keyCap:     DefaultKeyCap,
		ttl:        DefaultTTL,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ShipErrors — сколько батчей не удалось доставить.
func (s *Shipper) ShipErrors() uint64 { return s.shipErrs.Load() }

// Run — блокирующий цикл шиппера; запускается в горутине с safego.Recover
// на стороне вызывающего. По ctx.Done дослаёт накопленный хвост (со своим
// таймаутом) и возвращается.
func (s *Shipper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	batch := make([][]byte, 0, s.batchSize)
	for {
		select {
		case <-ctx.Done():
			s.drainTail(&batch)
			return
		case e := <-s.src:
			s.appendEntry(&batch, e)
			if len(batch) >= s.batchSize {
				s.flush(ctx, &batch)
			}
		case <-ticker.C:
			s.flush(ctx, &batch)
		}
	}
}

// appendEntry сериализует запись в батч; ошибка сериализации — дроп записи.
func (s *Shipper) appendEntry(batch *[][]byte, e Entry) {
	line, err := e.MarshalLine()
	if err != nil {
		s.noteError(err)
		return
	}
	*batch = append(*batch, line)
}

// drainTail забирает остаток канала (неблокирующе) и дослаёт батч с
// собственным таймаутом — исходный ctx уже отменён.
func (s *Shipper) drainTail(batch *[][]byte) {
	for {
		select {
		case e := <-s.src:
			s.appendEntry(batch, e)
		default:
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), finalFlushTimeout)
			s.flush(flushCtx, batch)
			cancel()
			return
		}
	}
}

func (s *Shipper) flush(ctx context.Context, batch *[][]byte) {
	if len(*batch) == 0 {
		return
	}
	if err := s.ship(ctx, *batch); err != nil {
		s.noteError(err)
	}
	*batch = (*batch)[:0]
}

// ship вызывает писателя, конвертируя его панику в ошибку: шиппер обязан
// пережить сбой писателя и продолжить работу. safego.Recover здесь не
// подходит — он логирует через slog, а лог-путь снова привёл бы запись в этот
// же шиппер (рекурсия); паника-как-ошибка уходит в noteError → stderr.
func (s *Shipper) ship(ctx context.Context, lines [][]byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("logsink: writer panic: %v", r)
		}
	}()
	return s.writer.Ship(ctx, s.key, lines, s.keyCap, s.ttl)
}

// noteError считает ошибку и изредка пишет диагностику в stderr напрямую
// (не через slog — иначе рекурсия обратно в шиппер).
func (s *Shipper) noteError(err error) {
	n := s.shipErrs.Add(1)
	if n == 1 || n%errLogEvery == 0 {
		fmt.Fprintf(os.Stderr, "logsink: ship %s failed (err #%d): %v\n", s.key, n, err)
	}
}
