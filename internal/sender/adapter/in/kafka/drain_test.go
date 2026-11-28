package kafka

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/sender/usecase"
)

// Мягкая остановка Sender'а (§118.8). До §118 обработка сообщения шла на том же
// контексте, что и чтение, а runner отменяет его первым: HTTP к получателю
// рвался посреди отправки, offset не коммитился, и после старта сообщение
// уходило снова — повтор на каждом деплое. Тесты ниже моделируют именно эту
// последовательность: «доставка в полёте» → отмена контекста приложения →
// Shutdown.

// drainReader — reader с одним сообщением: дальше FetchMessage ждёт отмены.
// Commit на отменённом контексте не проходит — как у настоящего reader'а.
// Реализует и slotReader, и sweepConsumer.
type drainReader struct {
	mu          sync.Mutex
	msgs        []kafka.Message
	committed   []int64
	closed      bool
	commitAfter bool // commit пришёл после Close — commit через закрытый reader
	// hangAfterClose — commit после Close ждёт только отмены ctx, как у
	// kafka-go: CommitMessages выбирает между буфером запросов и «reader
	// закрыт» случайно, и запрос, легший в буфер, никто уже не обслужит.
	hangAfterClose bool
}

func (r *drainReader) FetchMessage(ctx context.Context) (kafka.Message, error) {
	r.mu.Lock()
	if len(r.msgs) > 0 {
		m := r.msgs[0]
		r.msgs = r.msgs[1:]
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafka.Message{}, ctx.Err()
}

func (r *drainReader) Commit(ctx context.Context, msg kafka.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.commitAfter = true
		hang := r.hangAfterClose
		r.mu.Unlock()
		if hang {
			<-ctx.Done()
			return ctx.Err()
		}
		return errors.New("reader closed")
	}
	defer r.mu.Unlock()
	r.committed = append(r.committed, msg.Offset)
	return nil
}

func (r *drainReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *drainReader) snapshot() (committed []int64, commitAfterClose bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.committed...), r.commitAfter
}

// gatedDelivery — «HTTP-запрос к получателю»: идёт, пока его не отпустят
// (release) или не отменят контекст доставки (тогда — обрыв, как у настоящего
// HTTP-клиента, и Retry: offset не коммитится).
type gatedDelivery struct {
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	sawCancel bool
}

func newGatedDelivery() *gatedDelivery {
	return &gatedDelivery{started: make(chan struct{}), release: make(chan struct{})}
}

// deliver возвращает true, если доставка дошла до конца.
func (d *gatedDelivery) deliver(ctx context.Context) bool {
	d.once.Do(func() { close(d.started) })
	select {
	case <-d.release:
		return true
	case <-ctx.Done():
		d.mu.Lock()
		d.sawCancel = true
		d.mu.Unlock()
		return false
	}
}

func (d *gatedDelivery) cancelled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sawCancel
}

func (d *gatedDelivery) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-d.started:
	case <-time.After(5 * time.Second):
		t.Fatal("доставка так и не началась")
	}
}

func (d *gatedDelivery) Handle(ctx context.Context, _ []byte, _ map[string]string) usecase.HandleResult {
	if d.deliver(ctx) {
		return usecase.HandleAck
	}
	return usecase.HandleRetry
}

func (d *gatedDelivery) ProcessMessage(ctx context.Context, _ []byte, _ map[string]string) usecase.ReprocessResult {
	if d.deliver(ctx) {
		return usecase.ReprocessCommit
	}
	return usecase.ReprocessRetry
}

func waitClosed(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s не завершился", what)
	}
}

// TestConsumerGroup_Shutdown_DrainsInFlightDelivery: доставка, начатая до
// остановки, доигрывается и коммитится — ДО закрытия reader'а. Красный на коде
// до §118: отмена контекста приложения обрывала доставку, commit не случался.
func TestConsumerGroup_Shutdown_DrainsInFlightDelivery(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 7, Value: []byte(`{}`)}}}
	d := newGatedDelivery()
	g := newTestGroup(d, withReaderFactory(func(string) slotReader { return reader }))

	appCtx, cancelApp := context.WithCancel(context.Background())
	g.Start(appCtx)
	d.waitStarted(t)
	cancelApp() // runner: чтение остановлено — доставка в полёте

	budget, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Shutdown(budget)
	}()
	time.Sleep(50 * time.Millisecond) // Shutdown уже ждёт — получатель ещё отвечает
	close(d.release)
	waitClosed(t, done, "Shutdown")

	committed, afterClose := reader.snapshot()
	assert.False(t, d.cancelled(), "доставка не оборвана остановкой чтения")
	assert.Equal(t, []int64{7}, committed, "доставленное сообщение закоммичено — повтора после старта не будет")
	assert.False(t, afterClose, "commit — до закрытия reader'а")
}

// TestConsumerGroup_Shutdown_AbortsAfterCeiling: доставка дольше потолка
// обрывается (прежнее поведение), Shutdown не висит, commit не делается —
// сообщение перечитается после старта (at-least-once).
func TestConsumerGroup_Shutdown_AbortsAfterCeiling(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 9, Value: []byte(`{}`)}}}
	d := newGatedDelivery() // release не закрывается никогда
	g := newTestGroup(d, withReaderFactory(func(string) slotReader { return reader }))

	appCtx, cancelApp := context.WithCancel(context.Background())
	g.Start(appCtx)
	d.waitStarted(t)
	cancelApp()

	budget, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	g.Shutdown(budget)

	assert.Less(t, time.Since(start), 2*time.Second, "после потолка доставка обрывается, Shutdown не ждёт её конца")
	assert.True(t, d.cancelled(), "контекст доставки отменён по потолку")
	committed, _ := reader.snapshot()
	assert.Empty(t, committed, "оборванное сообщение не коммитится")
}

// TestDLQReprocessor_Shutdown_DrainsInFlightDelivery: то же для DLQ-репроцессора.
func TestDLQReprocessor_Shutdown_DrainsInFlightDelivery(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 3, Value: []byte(`{}`)}}}
	d := newGatedDelivery()
	r := newTestDLQReprocessor(reader, d)

	appCtx, cancelApp := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		r.Run(appCtx)
	}()
	d.waitStarted(t)
	cancelApp()

	budget, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Shutdown(budget, runDone)
	}()
	time.Sleep(50 * time.Millisecond)
	close(d.release)
	waitClosed(t, done, "Shutdown")

	committed, afterClose := reader.snapshot()
	assert.False(t, d.cancelled())
	assert.Equal(t, []int64{3}, committed)
	assert.False(t, afterClose)
}

// TestPausedSweeper_Shutdown_DrainsInFlightDelivery: то же для sweeper'а паузы
// (сообщение узла, снятого с паузы, доставляется из delay-топика).
func TestPausedSweeper_Shutdown_DrainsInFlightDelivery(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 5, Value: []byte(`{}`)}}}
	d := newGatedDelivery()
	s := newTestSweeper(reader, d, 10)

	appCtx, cancelApp := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		s.Run(appCtx)
	}()
	d.waitStarted(t)
	cancelApp()

	budget, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Shutdown(budget, runDone)
	}()
	time.Sleep(50 * time.Millisecond)
	close(d.release)
	waitClosed(t, done, "Shutdown")

	committed, afterClose := reader.snapshot()
	assert.False(t, d.cancelled())
	assert.Equal(t, []int64{5}, committed)
	assert.False(t, afterClose)
}

// stopMidDelivery — сценарий integration-хелперов (§118.8): доставка в полёте,
// контекст Run отменён, Stop закрыл reader, и только ПОСЛЕ этого получатель
// ответил. Возвращает канал выхода Run.
func stopMidDelivery(t *testing.T, run func(context.Context), stop func(), d *gatedDelivery) <-chan struct{} {
	t.Helper()
	appCtx, cancelApp := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		run(appCtx)
	}()
	d.waitStarted(t)
	cancelApp()
	stop()
	close(d.release)
	return runDone
}

// TestPausedSweeper_Stop_AbortsInFlightDelivery — регрессия CI выпуска 1.42.0
// (TestSender_PausedGate_StatusRoundTripTriggersRound висел, пока не истёк
// -timeout 45m): Stop закрывал reader, но доставку на контексте доставки не
// обрывал, и её commit уходил в закрытый reader навсегда. Красный на коде до
// исправления: Run не выходит.
func TestPausedSweeper_Stop_AbortsInFlightDelivery(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 5, Value: []byte(`{}`)}}, hangAfterClose: true}
	d := newGatedDelivery()
	s := newTestSweeper(reader, d, 10)

	waitClosed(t, stopMidDelivery(t, s.Run, s.Stop, d), "Run после Stop")
	committed, _ := reader.snapshot()
	assert.Empty(t, committed, "оборванная доставка не коммитится — перечитается после старта")
}

// TestDLQReprocessor_Stop_AbortsInFlightDelivery: то же для DLQ-репроцессора.
func TestDLQReprocessor_Stop_AbortsInFlightDelivery(t *testing.T) {
	t.Parallel()

	reader := &drainReader{msgs: []kafka.Message{{Offset: 3, Value: []byte(`{}`)}}, hangAfterClose: true}
	d := newGatedDelivery()
	r := newTestDLQReprocessor(reader, d)

	waitClosed(t, stopMidDelivery(t, r.Run, r.Stop, d), "Run после Stop")
	committed, _ := reader.snapshot()
	assert.Empty(t, committed, "оборванная доставка не коммитится — перечитается после старта")
}

// TestDeliveryScope_AbortBeforeBegin: остановленный компонент не доставляет —
// контекст, выданный после Abort, уже отменён.
func TestDeliveryScope_AbortBeforeBegin(t *testing.T) {
	t.Parallel()

	var d deliveryScope
	d.Abort()
	ctx, release := d.begin(context.Background())
	defer release()
	require.Error(t, ctx.Err())
}
