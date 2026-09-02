//go:build integration

package integration

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/logsink"
	"nexus/internal/platform/reloader"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	webredis "nexus/internal/web/adapter/out/redis"
	webuc "nexus/internal/web/usecase"
)

// Сценарные тесты консоли служебных логов (§51.7, ТЗ §51.8 п.2):
// шиппер → реальный Redis, мерж трёх ключей, кросс-сервисный reload уровня
// через реальный Redis pub/sub (закрывает исторический пробел — Subscriber.Run
// никогда не гонялся против реального Redis), маскировка e2e, неблокируемость
// лог-пути при зависшем писателе. Запуск: make test-int-logs.

// TestServiceLogs_ShipperToRealRedis: RingHandler → Shipper → Redis pipeline:
// LTRIM держит кольцо на keyCap, EXPIRE выставлен, индекс 0 — самая свежая
// запись, строки парсятся ридером вьювера.
func TestServiceLogs_ShipperToRealRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanupRedis := startRedis(t, ctx)
	defer cleanupRedis()

	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	ring := logsink.NewRingHandler(lv, "web")
	const keyCap = 50
	sh := logsink.NewShipper(logsink.NewRedisWriter(client), ring.Entries(), "web",
		logsink.WithKeyCap(keyCap), logsink.WithBatchSize(10),
		logsink.WithFlushInterval(50*time.Millisecond), logsink.WithTTL(time.Hour))
	shipCtx, shipCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); sh.Run(shipCtx) }()
	defer func() { shipCancel(); <-done }()

	logger := slog.New(ring)
	const total = 120
	for i := range total {
		logger.Info("line", slog.Int("i", i))
	}

	key := logsink.Key("web")
	reader := webredis.NewServiceLogReaderRedis(client, logging.NewNoop())
	// Ждём, пока шиппер отгрузит ВСЕ батчи. LTRIM держит список на keyCap уже
	// после 5-го батча (в голове i=49), поэтому одного `LLEN == keyCap` мало:
	// на медленном раннере голова ловится на середине отгрузки (напр. i=79 из
	// 120) — гонка. Корректное терминальное условие — самая свежая запись
	// (i=total-1) в голове списка (LPUSH-порядок). Attrs["i"] после JSON-раунда
	// через Redis — float64.
	require.Eventually(t, func() bool {
		n, err := client.LLen(ctx, key).Result()
		if err != nil || n != keyCap {
			return false
		}
		head, err := reader.Tail(ctx, "web", keyCap)
		if err != nil || len(head) != keyCap {
			return false
		}
		iv, ok := head[0].Attrs["i"].(float64)
		return ok && int(iv) == total-1
	}, 15*time.Second, 100*time.Millisecond, "shipper delivers all batches; head = freshest record")

	ttl, err := client.TTL(ctx, key).Result()
	require.NoError(t, err)
	assert.Positive(t, ttl, "EXPIRE must be set")
	assert.LessOrEqual(t, ttl, time.Hour)

	// Индекс 0 — самая свежая запись (LPUSH-порядок).
	entries, err := reader.Tail(ctx, "web", keyCap)
	require.NoError(t, err)
	require.Len(t, entries, keyCap)
	assert.Equal(t, "line", entries[0].Msg)
	assert.Equal(t, "web", entries[0].Service)
	assert.InDelta(t, total-1, entries[0].Attrs["i"], 0, "head of the list is the freshest record")
	assert.Equal(t, "info", entries[0].Level)
}

// TestServiceLogs_ViewerMergesThreeKeys: сырые JSON-строки в трёх ключах →
// реальный adapter + usecase → мерж по TS desc, limit, min_level.
func TestServiceLogs_ViewerMergesThreeKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanupRedis := startRedis(t, ctx)
	defer cleanupRedis()

	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	push := func(service, level string, ts time.Time, msg string) {
		e := logsink.Entry{TS: ts, Level: level, Service: service, Msg: msg}
		line, err := e.MarshalLine()
		require.NoError(t, err)
		require.NoError(t, client.LPush(ctx, logsink.Key(service), line).Err())
	}
	push("receiver", "info", base.Add(1*time.Second), "r0")
	push("receiver", "debug", base.Add(4*time.Second), "r1")
	push("sender", "warn", base.Add(5*time.Second), "s1")
	push("sender", "info", base.Add(2*time.Second), "s0")
	push("web", "error", base.Add(3*time.Second), "w0")

	uc := webuc.NewServiceLogsUsecase(webredis.NewServiceLogReaderRedis(client, logging.NewNoop()), logging.NewNoop())

	got, err := uc.Tail(ctx, nil, 0, "")
	require.NoError(t, err)
	msgs := make([]string, len(got))
	for i, e := range got {
		msgs[i] = e.Msg
	}
	assert.Equal(t, []string{"s1", "r1", "w0", "s0", "r0"}, msgs, "merged by TS desc across three keys")

	got, err = uc.Tail(ctx, nil, 3, "")
	require.NoError(t, err)
	require.Len(t, got, 3)

	got, err = uc.Tail(ctx, nil, 0, "warn")
	require.NoError(t, err)
	msgs = msgs[:0]
	for _, e := range got {
		msgs = append(msgs, e.Msg)
	}
	assert.Equal(t, []string{"s1", "w0"}, msgs, "min_level=warn keeps warn+error only")
}

// TestServiceLogs_ReloadLevelAcrossServices — сценарный: три in-process
// «сервиса» (LogController + reloader.Subscriber.Run с LogLevelReloader) +
// реальный AppSettingsUsecase.Update с реальным Publisher'ом. Смена уровня
// через PUT-путь доезжает до ВСЕХ трёх подписчиков без рестарта; nil →
// откат на YAML-fallback.
func TestServiceLogs_ReloadLevelAcrossServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, cleanupPG := startPostgres(t, ctx)
	defer cleanupPG()
	client, cleanupRedis := startRedis(t, ctx)
	defer cleanupRedis()

	// Три «сервиса»: YAML-fallback info (4), подписка на SectionLogging.
	services := []string{"receiver", "sender", "web"}
	ctls := make(map[string]*bootstrap.LogController, len(services))
	subCtx, subCancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, svc := range services {
		ctl := bootstrap.NewLogController(4, svc)
		ctls[svc] = ctl
		sub := reloader.NewSubscriber(client, logging.NewNoop())
		sub.Register(reloader.SectionLogging, bootstrap.LogLevelReloader(pool, ctl, mustTestCipher(t), logging.NewNoop()))
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub.Run(subCtx)
		}()
	}
	defer func() { subCancel(); wg.Wait() }()

	// Ждём фактической подписки всех трёх на канал (иначе publish уйдёт в никуда).
	require.Eventually(t, func() bool {
		subs, err := client.PubSubNumSub(ctx, reloader.Channel).Result()
		return err == nil && subs[reloader.Channel] == int64(len(services))
	}, 15*time.Second, 100*time.Millisecond, "all three subscribers must attach to the channel")

	// Web-сторона: реальный usecase + publisher (путь PUT /api/settings/app).
	uc := webuc.NewAppSettingsUsecase(
		pgrepo.NewAppSettingsRepoPg(pool, mustTestCipher(t), logging.NewNoop()),
		webuc.NewAuditUsecase(pgrepo.NewAuditRepoPg(pool, logging.NewNoop()), logging.NewNoop()),
		reloader.NewPublisher(client), false, 0, 0, logging.NewNoop())

	// updated_by — uuid-колонка: берём seed-пользователя admin из миграций.
	var adminID string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id::text FROM users WHERE login = 'admin'").Scan(&adminID))

	setLevel := func(l int) {
		require.NoError(t, uc.Update(ctx, webuc.Actor{UserID: adminID}, &domain.AppSettings{
			Logging: domain.LoggingSettings{Level: &l},
		}))
	}
	waitAll := func(want slog.Level, note string) {
		require.Eventually(t, func() bool {
			for _, ctl := range ctls {
				if ctl.Level.Level() != want {
					return false
				}
			}
			return true
		}, 20*time.Second, 100*time.Millisecond, note)
	}

	// Info → Error: глушится во всех трёх без рестарта.
	setLevel(2)
	waitAll(slog.LevelError, "level=2 must reach all three services")

	// Info-запись глушится, Error проходит — у каждого «сервиса».
	for svc, ctl := range ctls {
		logger := slog.New(ctl.Ring)
		before := len(ctl.Ring.Snapshot())
		logger.Info("suppressed")
		assert.Len(t, ctl.Ring.Snapshot(), before, "info must be suppressed at error level (%s)", svc)
		logger.Error("passes")
		assert.Len(t, ctl.Ring.Snapshot(), before+1, "error must pass (%s)", svc)
	}

	// Error → Debug: debug-записи снова проходят.
	setLevel(5)
	waitAll(slog.LevelDebug, "level=5 must reach all three services")
	for svc, ctl := range ctls {
		logger := slog.New(ctl.Ring)
		before := len(ctl.Ring.Snapshot())
		logger.Debug("visible")
		assert.Len(t, ctl.Ring.Snapshot(), before+1, "debug must pass after reload (%s)", svc)
	}
}

// TestServiceLogs_MaskingEndToEnd: чувствительные атрибуты маскируются ДО
// отправки — в Redis уезжает "***", секрет не покидает процесс.
func TestServiceLogs_MaskingEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanupRedis := startRedis(t, ctx)
	defer cleanupRedis()

	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	ring := logsink.NewRingHandler(lv, "web")
	sh := logsink.NewShipper(logsink.NewRedisWriter(client), ring.Entries(), "web",
		logsink.WithBatchSize(1), logsink.WithFlushInterval(50*time.Millisecond))
	shipCtx, shipCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); sh.Run(shipCtx) }()
	defer func() { shipCancel(); <-done }()

	logger := slog.New(ring)
	logger.Info("login attempt",
		slog.String("password", "hunter2"),
		slog.String("user", "bob"),
		slog.Group("auth", slog.String("token", "secret-token-value")))

	key := logsink.Key("web")
	require.Eventually(t, func() bool {
		n, err := client.LLen(ctx, key).Result()
		return err == nil && n >= 1
	}, 15*time.Second, 100*time.Millisecond)

	lines, err := client.LRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	raw := strings.Join(lines, "\n")
	assert.NotContains(t, raw, "hunter2", "password value must never reach Redis")
	assert.NotContains(t, raw, "secret-token-value", "nested token must never reach Redis")
	assert.Contains(t, raw, `"password":"***"`)
	assert.Contains(t, raw, `"token":"***"`)
	assert.Contains(t, raw, `"user":"bob"`, "non-sensitive attrs stay intact")
}

// hangingWriter — BatchWriter, висящий в Ship до отмены ctx: имитация
// зависшего Redis. Лог-путь при этом обязан жить (дроп, не блокировка).
type hangingWriter struct{}

func (hangingWriter) Ship(ctx context.Context, _ string, _ [][]byte, _ int64, _ time.Duration) error {
	<-ctx.Done()
	return errors.New("hanging writer: ctx done")
}

// TestServiceLogs_NonBlockingUnderLoad: 50 горутин × 200 записей при зависшем
// писателе — Handle не блокируется (жёсткий бюджет времени), дропы растут,
// горутины не текут.
func TestServiceLogs_NonBlockingUnderLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	ring := logsink.NewRingHandler(lv, "web", logsink.WithChannelCapacity(8), logsink.WithRingCapacity(64))
	sh := logsink.NewShipper(hangingWriter{}, ring.Entries(), "web",
		logsink.WithBatchSize(1), logsink.WithFlushInterval(10*time.Millisecond))
	shipCtx, shipCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); sh.Run(shipCtx) }()

	logger := slog.New(ring)
	const goroutines, perG = 50, 200
	start := time.Now()
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perG {
				logger.Info("load", slog.Int("g", g), slog.Int("i", i))
			}
		}()
	}
	waitDone := make(chan struct{})
	go func() { defer close(waitDone); wg.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(20 * time.Second):
		t.Fatal("log path blocked: writers did not finish under a hanging shipper")
	}
	elapsed := time.Since(start)
	t.Logf("10k records with hanging writer took %s, dropped=%d", elapsed, ring.Dropped())

	assert.Positive(t, ring.Dropped(), "overflow must be dropped, not block")
	assert.Len(t, ring.Snapshot(), 64, "in-process ring keeps working as a reserve")

	shipCancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shipper did not stop after ctx cancel")
	}
}
