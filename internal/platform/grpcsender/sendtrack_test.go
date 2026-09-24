package grpcsender

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	"nexus/internal/domain"
)

// TestCallState_SentAndExpiredExclusive — §109.3: отправка и истечение
// ready_wait взаимоисключающие. Отправленный вызов таймер не отменяет;
// отменённый таймером не считается отправленным, даже если OutHeader пришёл
// следом.
func TestCallState_SentAndExpiredExclusive(t *testing.T) {
	t.Parallel()

	sent := &callState{}
	sent.markSent()
	assert.False(t, sent.expire(), "отправленный вызов таймер не отменяет")
	assert.True(t, sent.sent())
	assert.False(t, sent.expired())

	expired := &callState{}
	assert.True(t, expired.expire())
	expired.markSent()
	assert.False(t, expired.sent(), "поздний OutHeader не делает вызов отправленным")
	assert.True(t, expired.expired())
	assert.False(t, expired.expire(), "истекает один раз")
}

// TestSendTracker — хендлер отмечает только клиентский OutHeader и задержку
// выбора транспорта; вызов без callState в контексте не трогает.
func TestSendTracker(t *testing.T) {
	t.Parallel()

	var h sendTracker
	st := &callState{}
	ctx := withCallState(context.Background(), st)

	h.HandleRPC(ctx, &stats.OutHeader{Client: false})
	assert.False(t, st.sent(), "серверный OutHeader — не отправка клиента")
	h.HandleRPC(ctx, &stats.DelayedPickComplete{})
	assert.True(t, st.pickDelayed())
	h.HandleRPC(ctx, &stats.OutHeader{Client: true})
	assert.True(t, st.sent())

	assert.NotPanics(t, func() {
		h.HandleRPC(context.Background(), &stats.OutHeader{Client: true})
	})
}

func stateOf(sent, expired, delayed bool) *callState {
	s := &callState{}
	switch {
	case sent:
		s.markSent()
	case expired:
		s.expire()
	}
	s.delayed.Store(delayed)
	return s
}

// TestWaitOutcome — какой исход ожидания попадает в счётчик
// nexus_sender_ready_wait_total. Соединение, числившееся READY, учитывается
// только если выбор транспорта блокировался (устаревший READY после падения
// Sender'а) или ожидание истекло.
func TestWaitOutcome(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name     string
		ctx      context.Context
		call     *callState
		wasReady bool
		want     string
	}{
		{name: "готовое соединение, ушёл сразу", ctx: context.Background(), call: stateOf(true, false, false), wasReady: true, want: ""},
		{name: "устаревший READY, выбор ждал", ctx: context.Background(), call: stateOf(true, false, true), wasReady: true, want: ReadyWaitReady},
		{name: "неготовое соединение, дождался", ctx: context.Background(), call: stateOf(true, false, false), want: ReadyWaitReady},
		{name: "истекло ожидание", ctx: context.Background(), call: stateOf(false, true, false), want: ReadyWaitTimeout},
		{name: "истекло на устаревшем READY", ctx: context.Background(), call: stateOf(false, true, false), wasReady: true, want: ReadyWaitTimeout},
		{name: "клиент ушёл во время ожидания", ctx: canceled, call: stateOf(false, false, false), want: ReadyWaitCanceled},
		{name: "не отправлен по иной причине", ctx: context.Background(), call: stateOf(false, false, false), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, waitOutcome(tc.ctx, tc.call, tc.wasReady))
		})
	}
}

// TestTranslateError — §109.3/§109.4: ErrSenderUnavailable только для
// неотправленного вызова; обрыв отправленного остаётся прежней ошибкой (502).
func TestTranslateError(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	unavailable := status.Error(codes.Unavailable, "connection refused")
	cases := []struct {
		name            string
		ctx             context.Context
		err             error
		call            *callState
		wantUnavailable bool
		wantCanceled    bool
		wantSame        bool
	}{
		{name: "клиент ушёл", ctx: canceled, err: status.Error(codes.Canceled, "context canceled"), call: stateOf(false, false, false), wantCanceled: true},
		{name: "ожидание истекло", ctx: context.Background(), err: status.Error(codes.Canceled, "context canceled"), call: stateOf(false, true, false), wantUnavailable: true},
		{name: "Unavailable до отправки", ctx: context.Background(), err: unavailable, call: stateOf(false, false, false), wantUnavailable: true},
		{name: "Unavailable после отправки", ctx: context.Background(), err: unavailable, call: stateOf(true, false, false), wantSame: true},
		{name: "иная ошибка", ctx: context.Background(), err: status.Error(codes.Internal, "boom"), call: stateOf(false, false, false), wantSame: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := translateError(tc.ctx, tc.err, tc.call, 15*time.Second)
			assert.Equal(t, tc.wantUnavailable, errors.Is(got, domain.ErrSenderUnavailable))
			assert.Equal(t, tc.wantCanceled, errors.Is(got, context.Canceled))
			if tc.wantSame {
				assert.Equal(t, tc.err, got)
			}
			assert.Equal(t, status.Code(tc.err), status.Code(got), "gRPC-код остаётся в цепочке")
		})
	}
}
