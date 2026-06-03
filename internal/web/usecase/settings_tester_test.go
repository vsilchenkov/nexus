package usecase

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
)

// fakeCHConn — минимум для теста SettingsTester: Ping + Close. Прочие методы
// chdriver.Conn не вызываются на пути TestClickHouse.
type fakeCHConn struct {
	pingErr error
	closed  atomic.Bool
}

func (f *fakeCHConn) Close() error                                  { f.closed.Store(true); return nil }
func (f *fakeCHConn) Ping(context.Context) error                    { return f.pingErr }
func (*fakeCHConn) Contributors() []string                          { panic("not impl") }
func (*fakeCHConn) ServerVersion() (*chdriver.ServerVersion, error) { panic("not impl") }
func (*fakeCHConn) Select(context.Context, any, string, ...any) error {
	panic("not impl")
}
func (*fakeCHConn) Query(context.Context, string, ...any) (chdriver.Rows, error) {
	panic("not impl")
}
func (*fakeCHConn) QueryRow(context.Context, string, ...any) chdriver.Row { panic("not impl") }
func (*fakeCHConn) PrepareBatch(context.Context, string, ...chdriver.PrepareBatchOption) (chdriver.Batch, error) {
	panic("not impl")
}
func (*fakeCHConn) Exec(context.Context, string, ...any) error              { panic("not impl") }
func (*fakeCHConn) AsyncInsert(context.Context, string, bool, ...any) error { panic("not impl") }
func (*fakeCHConn) Stats() chdriver.Stats                                   { panic("not impl") }

// fakeSentryClient — реализует SentryTestClient без сети.
type fakeSentryClient struct {
	flushOK  bool
	captured atomic.Int32
}

func (f *fakeSentryClient) CaptureMessage(string, *sentry.EventHint, sentry.EventModifier) *sentry.EventID {
	f.captured.Add(1)
	id := sentry.EventID("test-event")
	return &id
}
func (f *fakeSentryClient) Flush(time.Duration) bool { return f.flushOK }

func emptyRepo() *fakeAppSettingsRepo {
	return &fakeAppSettingsRepo{current: &domain.AppSettings{}}
}

func newTester(t *testing.T,
	repo *fakeAppSettingsRepo,
	chFactory ClickHouseFactory,
	sentryFactory SentryClientFactory,
) *SettingsTester {
	t.Helper()
	cfg := &config.Config{
		ClickHouse: config.ClickHouseSection{Host: "yaml-host", Port: 9000, Database: "yaml-db"},
		Sentry:     config.SentrySection{Use: false, Dsn: ""},
	}
	return NewSettingsTester(repo, cfg, chFactory, sentryFactory, nil, "Test", "v0", logging.NewNoop())
}

func TestSettingsTester_TestTelegram(t *testing.T) {
	t.Parallel()
	token := "bot-token"
	chat := "-100500"
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{
		Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{
			BotToken: &token, ChatID: &chat,
		}},
	}}
	cfg := &config.Config{}
	sender := &notifSender{}
	tester := NewSettingsTester(repo, cfg, nil, nil, sender, "Test", "v0", logging.NewNoop())

	// Маскированный токен в patch не используется — merge берёт сохранённый.
	masked := "***"
	res, err := tester.TestTelegram(context.Background(), &domain.TelegramSettings{BotToken: &masked})
	require.NoError(t, err)
	require.True(t, res.OK)
	require.Len(t, sender.msgs, 1)
}

func TestSettingsTester_TestTelegram_NoToken(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}}
	tester := NewSettingsTester(repo, &config.Config{}, nil, nil, &notifSender{}, "Test", "v0", logging.NewNoop())
	res, err := tester.TestTelegram(context.Background(), &domain.TelegramSettings{})
	require.NoError(t, err)
	require.False(t, res.OK)
}

func TestSettingsTester_TestClickHouse_Success(t *testing.T) {
	t.Parallel()
	conn := &fakeCHConn{}
	called := 0
	factory := func(_ context.Context, c *config.ClickHouseSection) (chdriver.Conn, error) {
		called++
		// patch с новым host'ом должен дойти до factory.
		assert.Equal(t, "patched-host", c.Host)
		assert.Equal(t, 9001, c.Port)
		return conn, nil
	}
	tester := newTester(t, emptyRepo(), factory, nil)

	host := "patched-host"
	port := 9001
	res, err := tester.TestClickHouse(context.Background(),
		&domain.ClickHouseSettings{Host: &host, Port: &port})

	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Empty(t, res.Error)
	assert.Equal(t, 1, called)
	assert.True(t, conn.closed.Load(), "conn must be closed after test")
}

func TestSettingsTester_TestClickHouse_FactoryError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("dial refused")
	factory := func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		return nil, wantErr
	}
	tester := newTester(t, emptyRepo(), factory, nil)

	res, err := tester.TestClickHouse(context.Background(), &domain.ClickHouseSettings{})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "dial refused")
}

func TestSettingsTester_TestClickHouse_PingError(t *testing.T) {
	t.Parallel()
	conn := &fakeCHConn{pingErr: errors.New("ping timeout")}
	factory := func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		return conn, nil
	}
	tester := newTester(t, emptyRepo(), factory, nil)

	res, err := tester.TestClickHouse(context.Background(), &domain.ClickHouseSettings{})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "ping timeout")
	assert.True(t, conn.closed.Load())
}

func TestSettingsTester_TestClickHouse_MergesCurrentSettings(t *testing.T) {
	t.Parallel()
	curPass := "stored-secret"
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{
		ClickHouse: domain.ClickHouseSettings{Password: &curPass},
	}}
	conn := &fakeCHConn{}
	factory := func(_ context.Context, c *config.ClickHouseSection) (chdriver.Conn, error) {
		// Patch только host; password должен подтянуться из current settings.
		assert.Equal(t, "stored-secret", c.Password)
		assert.Equal(t, "new-host", c.Host)
		return conn, nil
	}
	tester := newTester(t, repo, factory, nil)

	host := "new-host"
	res, err := tester.TestClickHouse(context.Background(),
		&domain.ClickHouseSettings{Host: &host})
	require.NoError(t, err)
	assert.True(t, res.OK)
}

func TestSettingsTester_TestSentry_DisabledReturnsFalse(t *testing.T) {
	t.Parallel()
	factory := func(sentry.ClientOptions) (SentryTestClient, error) {
		t.Fatal("factory must not be called when use=false")
		return nil, nil
	}
	tester := newTester(t, emptyRepo(), nil, factory)

	useFalse := false
	res, err := tester.TestSentry(context.Background(), &domain.SentrySettings{Use: &useFalse})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "disabled")
}

func TestSettingsTester_TestSentry_EmptyDSN(t *testing.T) {
	t.Parallel()
	useTrue := true
	tester := newTester(t, emptyRepo(), nil, func(sentry.ClientOptions) (SentryTestClient, error) {
		t.Fatal("factory must not be called when DSN empty")
		return nil, nil
	})
	res, err := tester.TestSentry(context.Background(), &domain.SentrySettings{Use: &useTrue})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "DSN is empty")
}

func TestSettingsTester_TestSentry_Success(t *testing.T) {
	t.Parallel()
	client := &fakeSentryClient{flushOK: true}
	called := 0
	factory := func(opts sentry.ClientOptions) (SentryTestClient, error) {
		called++
		assert.Equal(t, "https://abc@sentry/1", opts.Dsn)
		return client, nil
	}
	tester := newTester(t, emptyRepo(), nil, factory)

	useTrue := true
	dsn := "https://abc@sentry/1"
	res, err := tester.TestSentry(context.Background(),
		&domain.SentrySettings{Use: &useTrue, DSN: &dsn})
	require.NoError(t, err)
	assert.True(t, res.OK)
	assert.Equal(t, 1, called)
	assert.Equal(t, int32(1), client.captured.Load())
}

func TestSettingsTester_TestSentry_FlushTimeout(t *testing.T) {
	t.Parallel()
	client := &fakeSentryClient{flushOK: false}
	factory := func(sentry.ClientOptions) (SentryTestClient, error) { return client, nil }
	tester := newTester(t, emptyRepo(), nil, factory)

	useTrue := true
	dsn := "https://abc@sentry/1"
	res, err := tester.TestSentry(context.Background(),
		&domain.SentrySettings{Use: &useTrue, DSN: &dsn})
	require.NoError(t, err)
	assert.False(t, res.OK)
	assert.Contains(t, res.Error, "flush")
}

func TestSettingsTester_RepoFailure_Propagates(t *testing.T) {
	t.Parallel()
	repo := &fakeAppSettingsRepo{current: &domain.AppSettings{}, getErr: errors.New("db down")}
	tester := newTester(t, repo, func(context.Context, *config.ClickHouseSection) (chdriver.Conn, error) {
		return nil, nil
	}, nil)

	_, err := tester.TestClickHouse(context.Background(), &domain.ClickHouseSettings{})
	require.Error(t, err)
}
