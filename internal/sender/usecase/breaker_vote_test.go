package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/usecase/port"
)

// Тесты §81.2: отмена ВЫЗЫВАЮЩЕЙ стороны не должна считаться отказом приёмника.
//
// Боевой сценарий, из которого выросло правило (§81.1): приёмник отвечал 200 за
// 145 с, соединение обрывали на 50-й секунде, пять обрывов подряд открывали
// breaker — и выйти из этого было нельзя, потому что половинчато-открытая проба
// обрывалась ровно так же.

// stubCanceledCaller — вызов, во время которого умирает родительский контекст
// (клиент ушёл), с ошибкой в точности как у net/http: обёрнутый context.Canceled.
type stubCanceledCaller struct {
	cancel context.CancelFunc
	calls  int
}

func (s *stubCanceledCaller) Do(ctx context.Context, req *port.HTTPRequest) (*port.HTTPResponse, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	url := ""
	if req != nil {
		url = req.URL
	}
	return nil, fmt.Errorf("Post %q: %w", url, context.Canceled)
}

// stubDeadlineCaller — истёк НАШ таймаут узла: родительский контекст жив.
type stubDeadlineCaller struct{ calls int }

func (s *stubDeadlineCaller) Do(_ context.Context, req *port.HTTPRequest) (*port.HTTPResponse, error) {
	s.calls++
	url := ""
	if req != nil {
		url = req.URL
	}
	return nil, fmt.Errorf("Post %q: %w", url, context.DeadlineExceeded)
}

func TestSend_CallerGone_BreakerNotTouched(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb := &stubBreaker{allow: true}
	httpc := &stubCanceledCaller{cancel: cancel}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)

	uc.Send(ctx, baseInput())

	assert.Zero(t, cb.failureCount, "уход клиента — не отказ приёмника")
	assert.Zero(t, cb.successCount, "и не успех: о здоровье узла исход не говорит ничего")
}

// Прямой регресс на боевой инцидент: пять обрывов подряд не должны открывать
// breaker живого приёмника.
func TestSend_CallerGoneRepeatedly_NeverOpensBreaker(t *testing.T) {
	t.Parallel()

	cb := &stubBreaker{allow: true}
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		httpc := &stubCanceledCaller{cancel: cancel}
		uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)
		uc.Send(ctx, baseInput())
		cancel()
	}

	assert.Zero(t, cb.failureCount, "серия обрывов клиента не приближает открытие breaker'а")
}

// Наш собственный таймаут узла — счёт приёмнику: он не уложился в срок.
func TestSend_UpstreamTimeout_CountsAsFailure(t *testing.T) {
	t.Parallel()

	cb := &stubBreaker{allow: true}
	logw := &stubLogWriter{}
	uc := NewSendUsecase(&stubDeadlineCaller{}, logw, cb, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.TimeoutMs = 30000
	out := uc.Send(context.Background(), in)

	assert.Equal(t, 1, cb.failureCount)
	assert.Zero(t, cb.successCount)
	assert.True(t, out.Timeout, "Receiver отдаёт 504 именно по этому признаку")

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, strings.HasPrefix(rec.Reason, domain.ReasonUpstreamTimeout+":"),
		"маркер таймаута, а не сырой текст stdlib: %q", rec.Reason)
	assert.Contains(t, rec.Reason, "30000", "в детали — сам таймаут узла")
}

// Регресс на «не переклассифицировали лишнего»: мёртвый адрес по-прежнему
// открывает breaker, иначе защита перестала бы работать вовсе.
func TestSend_TransportError_StillCountsAsFailure(t *testing.T) {
	t.Parallel()

	cb := &stubBreaker{allow: true}
	httpc := &stubHTTPCaller{errs: []error{errors.New("dial tcp 10.0.0.1:443: connect: connection refused")}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)

	uc.Send(context.Background(), baseInput())

	assert.Equal(t, 1, cb.failureCount, "отказ соединения — настоящий признак мёртвого адреса")
}

// Учёт исхода обязан выполняться на ЖИВОМ контексте, даже когда родительский
// уже мёртв: go-redis отбрасывает команду с отменённым контекстом ещё в пуле
// соединений, и запись молча не выполнялась бы.
func TestSend_Bookkeeping_RunsOnLiveContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb := &stubBreaker{allow: true}
	// Клиент ушёл ВО ВРЕМЯ вызова, но ответ 500 успел прийти: приёмник нездоров,
	// отказ засчитать надо — и записать его есть чем только на живом контексте.
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 500}}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)
	cancel()

	uc.Send(ctx, baseInput())

	require.Len(t, cb.failureCtxErrs, 1, "отказ должен быть учтён")
	assert.NoError(t, cb.failureCtxErrs[0], "контекст учёта отвязан от мёртвого родителя")
}

func TestSend_Bookkeeping_SuccessAlsoOnLiveContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb := &stubBreaker{allow: true}
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 200}}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)
	cancel()

	uc.Send(ctx, baseInput())

	require.Len(t, cb.successCtxErrs, 1)
	assert.NoError(t, cb.successCtxErrs[0])
}

// §68/§81.2.1: в журнал не должен попадать адрес узла — у динамического URL его
// хвост собран из входящего запроса.
func TestSend_CallerGone_ReasonCarriesNoURL(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logw := &stubLogWriter{}
	uc := NewSendUsecase(&stubCanceledCaller{cancel: cancel}, logw, &stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.TargetURL = "https://secret-host.example.com/hs/webhook/tenant-42?token=abc"
	uc.Send(ctx, in)

	require.Len(t, logw.written, 1)
	rec := logw.written[0].rec
	assert.True(t, strings.HasPrefix(rec.Reason, domain.ReasonClientCanceled+":"),
		"стабильный маркер: %q", rec.Reason)
	assert.NotContains(t, rec.Reason, "secret-host.example.com")
	assert.NotContains(t, rec.Reason, "token=abc")
	// Тот же маркер обязан стоять и в поэлементных причинах, иначе сырой текст
	// утечёт мимо нормализации через attempts_details.
	assert.NotContains(t, rec.AttemptsDetails, "secret-host.example.com")
	assert.Contains(t, rec.AttemptsDetails, domain.ReasonClientCanceled)
}

func TestClassifyUpstream(t *testing.T) {
	t.Parallel()

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	tests := []struct {
		name    string
		ctx     context.Context
		resp    *port.HTTPResponse
		lastErr error
		want    breakerVote
	}{
		{"2xx — узел жив", live, &port.HTTPResponse{StatusCode: 200}, nil, voteHealthy},
		{"4xx — узел жив (§50.4)", live, &port.HTTPResponse{StatusCode: 422}, nil, voteHealthy},
		{"oversize по факту 200 (§43)", live, &port.HTTPResponse{StatusCode: 200, TooLarge: true}, nil, voteHealthy},
		{"5xx — нездоров", live, &port.HTTPResponse{StatusCode: 503}, nil, voteUnhealthy},
		{"наш таймаут — нездоров", live, nil, context.DeadlineExceeded, voteUnhealthy},
		{"отказ соединения — нездоров", live, nil, errors.New("connection refused"), voteUnhealthy},
		{"клиент ушёл — воздержаться", dead, nil, context.Canceled, voteAbstain},
		// Порядок веток: живость родителя проверяется РАНЬШЕ признака дедлайна,
		// иначе чужой дедлайн выше по стеку снова засчитается приёмнику.
		{"чужой дедлайн выше по стеку — воздержаться", dead, nil, context.DeadlineExceeded, voteAbstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, classifyUpstream(tt.ctx, tt.resp, tt.lastErr))
		})
	}
}

// §81.3: политика узла обязана доехать до учёта отказа. Без этого настройка в
// карточке узла была бы косметической — breaker считал бы по глобальной.
func TestSend_NodeBreakerPolicy_ReachesRecordFailure(t *testing.T) {
	t.Parallel()

	cb := &stubBreaker{allow: true}
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 503}}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)

	in := baseInput()
	in.BreakerThreshold = 2
	in.BreakerCooldownSec = 7
	uc.Send(context.Background(), in)

	require.Len(t, cb.failurePolicies, 1)
	assert.Equal(t, 2, cb.failurePolicies[0].Threshold)
	assert.Equal(t, 7*time.Second, cb.failurePolicies[0].Cooldown)
}

// Узел без собственной политики отдаёт нули — реализация трактует их как
// «глобальная из конфигурации» (проверено в circuitbreaker/admin_test.go).
func TestSend_NoNodePolicy_PassesZeroes(t *testing.T) {
	t.Parallel()

	cb := &stubBreaker{allow: true}
	httpc := &stubHTTPCaller{responses: []*port.HTTPResponse{{StatusCode: 500}}}
	uc := NewSendUsecase(httpc, &stubLogWriter{}, cb, logging.NewNoop(), 64<<20)

	uc.Send(context.Background(), baseInput())

	require.Len(t, cb.failurePolicies, 1)
	assert.Zero(t, cb.failurePolicies[0].Threshold)
	assert.Zero(t, cb.failurePolicies[0].Cooldown)
}

// Ревизия §81.9: уход вызывающей стороны не должен красить узел в «Down».
// Исход последнего вызова о здоровье приёмника в этом случае не говорит ничего,
// а бейдж §52 живёт 30 суток и снимается только следующим фактическим вызовом —
// узел, чьи клиенты не дожидаются ответа, горел бы красным вечно. До §81.2 эта
// запись просто не доезжала до Redis (мёртвый контекст), и дефект был не виден.
func TestSend_CallerGone_MarksOutputSoNodeIsNotPaintedDown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	uc := NewSendUsecase(&stubCanceledCaller{cancel: cancel}, &stubLogWriter{},
		&stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	out := uc.Send(ctx, baseInput())

	assert.True(t, out.CallerGone, "адаптеры по этому признаку пропускают запись исхода узла")
	assert.EqualValues(t, 0, out.StatusCode, "статуса нет — приёмник не ответил")
}

// А вот настоящий отказ обязан признак НЕ ставить, иначе мы перестанем красить
// действительно мёртвые узлы.
func TestSend_RealFailure_DoesNotMarkCallerGone(t *testing.T) {
	t.Parallel()

	uc := NewSendUsecase(&stubDeadlineCaller{}, &stubLogWriter{},
		&stubBreaker{allow: true}, logging.NewNoop(), 64<<20)

	out := uc.Send(context.Background(), baseInput())

	assert.False(t, out.CallerGone, "наш таймаут — счёт приёмнику, узел красим")
}
