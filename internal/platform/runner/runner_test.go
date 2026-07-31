package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/platform/logging"
)

// Тестируется внутренний program (реализация service.Interface), а не Run:
// Run отдаёт управление kardianos/service, который под тестом уходит в SCM или
// в ожидание сигнала. Контракт program и есть то, что должно держаться:
// Start не блокирует, Stop отменяет контекст приложения и зовёт его Stop.

// fakeApp — приложение с управляемым поведением Start/Stop.
type fakeApp struct {
	mu sync.Mutex

	startErr   error
	startPanic bool

	started  chan struct{}
	ctxDone  chan struct{}
	stopCall chan context.Context
}

func newFakeApp() *fakeApp {
	return &fakeApp{
		started:  make(chan struct{}, 1),
		ctxDone:  make(chan struct{}, 1),
		stopCall: make(chan context.Context, 1),
	}
}

func (a *fakeApp) Start(ctx context.Context) error {
	a.started <- struct{}{}
	a.mu.Lock()
	shouldPanic, err := a.startPanic, a.startErr
	a.mu.Unlock()

	if shouldPanic {
		panic("app blew up")
	}
	if err != nil {
		return err
	}
	<-ctx.Done() // блокирующий Start, как требует контракт App
	a.ctxDone <- struct{}{}
	return nil
}

func (a *fakeApp) Stop(ctx context.Context) error {
	a.stopCall <- ctx
	return nil
}

func newProgram(app App) *program {
	return &program{app: app, logger: logging.NewNoop()}
}

func TestProgramStartIsNonBlockingAndRunsApp(t *testing.T) {
	t.Parallel()

	app := newFakeApp()
	p := newProgram(app)

	require.NoError(t, p.Start(nil), "Start обязан вернуться сразу: его вызывает SCM")

	select {
	case <-app.started:
	case <-time.After(2 * time.Second):
		t.Fatal("app.Start не был вызван")
	}

	require.NoError(t, p.Stop(nil))
}

func TestProgramStopCancelsAppContext(t *testing.T) {
	t.Parallel()

	app := newFakeApp()
	p := newProgram(app)

	require.NoError(t, p.Start(nil))
	<-app.started

	require.NoError(t, p.Stop(nil))

	select {
	case <-app.ctxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop не отменил контекст, переданный в app.Start — graceful shutdown не сработает")
	}

	select {
	case ctx := <-app.stopCall:
		assert.NotNil(t, ctx)
		assert.NoError(t, ctx.Err(), "в app.Stop идёт живой контекст: остановка должна успеть доработать")
	case <-time.After(2 * time.Second):
		t.Fatal("app.Stop не был вызван")
	}
}

// Ошибка Start логируется и не роняет процесс: сам факт возврата ошибки не
// должен превращаться в панику или зависание.
func TestProgramStartErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	app := newFakeApp()
	app.startErr = errors.New("boom")
	p := newProgram(app)

	require.NoError(t, p.Start(nil))
	<-app.started

	require.NoError(t, p.Stop(nil))
}

// §30.2: паника в горутине приложения гасится safego.Recover — процесс обязан
// пережить её, иначе один сбойный старт валит весь сервис.
func TestProgramSurvivesPanicInApp(t *testing.T) {
	t.Parallel()

	app := newFakeApp()
	app.startPanic = true
	p := newProgram(app)

	require.NoError(t, p.Start(nil))
	<-app.started

	// Если бы recover отсутствовал, паника из горутины убила бы тестовый процесс.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, p.Stop(nil))
}

// Stop до Start не должен разыменовывать nil-cancel.
func TestProgramStopBeforeStart(t *testing.T) {
	t.Parallel()

	app := newFakeApp()
	p := newProgram(app)

	assert.NotPanics(t, func() { _ = p.Stop(nil) })
}
