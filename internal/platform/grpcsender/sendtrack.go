package grpcsender

import (
	"context"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/stats"
)

// Состояния вызова для callState (§109.3).
const (
	callPending int32 = iota // заголовки ещё не ушли в транспорт
	callSent                 // стрим создан — вызов мог дойти до Sender'а
	callExpired              // ready_wait истёк раньше отправки — вызов отменён
)

// callState — отправлен ли вызов (§109.3). Это единственный надёжный признак,
// можно ли честно сказать клиенту «повторите»: состояние соединения на момент
// вызова врёт (сразу после падения Sender'а соединение ещё числится READY), а
// код Unavailable одинаков для «транспорта не было» и «транспорт оборвался
// посреди вызова».
//
// Переходы только из callPending и только один раз: отправка и истечение
// ожидания конкурируют через CAS, так что вызов, отменённый по ready_wait,
// не считается отправленным, а отправленный таймер уже не отменит.
type callState struct {
	v       atomic.Int32
	delayed atomic.Bool  // выбор транспорта блокировался (ждали готовности)
	sentAt  atomic.Int64 // unix-нс отправки; 0 — не отправлен
}

// markSent фиксирует создание стрима. Вызывается из stats-хендлера.
func (s *callState) markSent() {
	if s.v.CompareAndSwap(callPending, callSent) {
		s.sentAt.Store(time.Now().UnixNano())
	}
}

// expire фиксирует истечение ready_wait. true — вызов ещё не был отправлен,
// и его можно отменять.
func (s *callState) expire() bool { return s.v.CompareAndSwap(callPending, callExpired) }

func (s *callState) sent() bool        { return s.v.Load() == callSent }
func (s *callState) expired() bool     { return s.v.Load() == callExpired }
func (s *callState) pickDelayed() bool { return s.delayed.Load() }

// waited — сколько вызов ждал отправки: до создания стрима, а если стрима не
// было — до сих пор.
func (s *callState) waited(started time.Time) time.Duration {
	if at := s.sentAt.Load(); at != 0 {
		return time.Unix(0, at).Sub(started)
	}
	return time.Since(started)
}

type callStateKey struct{}

func withCallState(ctx context.Context, s *callState) context.Context {
	return context.WithValue(ctx, callStateKey{}, s)
}

func callStateFrom(ctx context.Context) *callState {
	s, _ := ctx.Value(callStateKey{}).(*callState)
	return s
}

// sendTracker — stats.Handler, отмечающий в callState ход вызова (§109.3):
//
//   - OutHeader клиента gRPC отдаёт, когда заголовки стрима поставлены в
//     очередь живого транспорта (http2Client.NewStream): раньше этого у
//     Sender'а не может появиться ничего. Попытки, не дошедшие до транспорта
//     (соединение закрылось, GOAWAY), gRPC повторяет прозрачно, и OutHeader у
//     них нет;
//   - DelayedPickComplete — выбор транспорта блокировался и завершился: вызов
//     ждал готовности, даже если соединение на старте числилось READY.
type sendTracker struct{}

var _ stats.Handler = sendTracker{}

func (sendTracker) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }

func (sendTracker) HandleRPC(ctx context.Context, s stats.RPCStats) {
	st := callStateFrom(ctx)
	if st == nil {
		return
	}
	switch ev := s.(type) {
	case *stats.OutHeader:
		if ev.Client {
			st.markSent()
		}
	case *stats.DelayedPickComplete:
		st.delayed.Store(true)
	}
}

func (sendTracker) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context { return ctx }

func (sendTracker) HandleConn(context.Context, stats.ConnStats) {}
