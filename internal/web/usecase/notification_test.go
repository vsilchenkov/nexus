package usecase

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ---- fakes для планировщика уведомлений ----

type fakeSettingsReader struct{ s *domain.AppSettings }

func (f fakeSettingsReader) Raw(context.Context) (*domain.AppSettings, error) { return f.s, nil }

type notifTeamRepo struct {
	nopTeamRepo
	teams []*domain.Team
}

func (r notifTeamRepo) List(context.Context) ([]*domain.Team, error) { return r.teams, nil }

type notifNodeRepo struct {
	byTeam map[string][]*domain.Node
}

func (r notifNodeRepo) List(_ context.Context, f port.ListNodesFilter) ([]*domain.Node, error) {
	return r.byTeam[f.TeamID], nil
}
func (r notifNodeRepo) Get(context.Context, string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r notifNodeRepo) GetByPath(context.Context, string) (*domain.Node, error) {
	return nil, domain.ErrNodeNotFound
}
func (r notifNodeRepo) Count(context.Context, string) (int, error) { return 0, nil }
func (r notifNodeRepo) Create(context.Context, *domain.Node) error { return nil }
func (r notifNodeRepo) Update(context.Context, *domain.Node) error { return nil }
func (r notifNodeRepo) Delete(context.Context, string) error       { return nil }
func (r notifNodeRepo) UpdateAllowedHostsSnapshot(context.Context, string, []string) error {
	return nil
}

// notifProm — фейк PromMetrics: per-node «незавершённые» вызовы (§22).
type notifProm struct {
	errs map[string]float64
	err  error
}

func (p notifProm) GlobalTotals(context.Context, time.Duration) (port.GlobalTotals, error) {
	return port.GlobalTotals{}, nil
}
func (p notifProm) KafkaQueue(context.Context) (float64, error) { return 0, nil }
func (p notifProm) NodeThroughput(context.Context, time.Duration) (map[string]port.NodeThroughput, error) {
	return nil, nil
}
func (p notifProm) NodeErrors(context.Context, time.Duration) (map[string]float64, error) {
	return p.errs, p.err
}
func (p notifProm) NodeSeries(context.Context, time.Duration, int) (map[string][]float64, error) {
	return nil, nil
}

type notifSender struct {
	msgs []string
	err  error
}

func (s *notifSender) Send(_ context.Context, _, _, text string) error {
	if s.err != nil {
		return s.err
	}
	s.msgs = append(s.msgs, text)
	return nil
}

type fakeLock struct{ ok bool }

func (l fakeLock) TryLock(context.Context, time.Duration) (bool, error) { return l.ok, nil }

type memCheckpoint struct {
	v    int64
	sets int
}

func (c *memCheckpoint) Get(context.Context) (int64, error) { return c.v, nil }
func (c *memCheckpoint) Set(_ context.Context, ms int64) error {
	c.v = ms
	c.sets++
	return nil
}

func notifSettings(enabled bool, cronExpr string) *domain.AppSettings {
	e, tok, chat := enabled, "tok", "-100"
	return &domain.AppSettings{Notifications: domain.NotificationsSettings{Telegram: domain.TelegramSettings{
		Enabled: &e, BotToken: &tok, ChatID: &chat, Cron: &cronExpr,
	}}}
}

func newScheduler(s *domain.AppSettings, nodes port.NodeRepo, prom port.PromMetrics, sender *notifSender, lock fakeLock, cp *memCheckpoint) *NotificationScheduler {
	teams := notifTeamRepo{teams: []*domain.Team{{ID: "t1", Slug: "default", Name: "Default", CHDatabase: "nexus_default"}}}
	return NewNotificationScheduler(fakeSettingsReader{s}, teams, nodes, prom, sender, lock, cp, logging.NewNoop())
}

func oneNode() notifNodeRepo {
	return notifNodeRepo{byTeam: map[string][]*domain.Node{
		"t1": {{Path: "svc/hook", ClickHouseTable: "nexus_default.logs", TeamID: "t1"}},
	}}
}

func TestNotifCycle_SendsWhenErrors(t *testing.T) {
	t.Parallel()
	sender := &notifSender{}
	cp := &memCheckpoint{}
	s := newScheduler(notifSettings(true, "*/5 * * * *"), oneNode(),
		notifProm{errs: map[string]float64{"svc/hook": 5}}, sender, fakeLock{ok: true}, cp)

	s.cycle(context.Background())
	require.Len(t, sender.msgs, 1)
	assert.Contains(t, sender.msgs[0], "total errors: 5")
	assert.Contains(t, sender.msgs[0], "svc/hook")
	assert.Equal(t, 1, cp.sets, "checkpoint advanced after send")
}

func TestNotifCycle_NoErrorsNoSend(t *testing.T) {
	t.Parallel()
	sender := &notifSender{}
	cp := &memCheckpoint{}
	s := newScheduler(notifSettings(true, "*/5 * * * *"), oneNode(),
		notifProm{errs: map[string]float64{}}, sender, fakeLock{ok: true}, cp)

	s.cycle(context.Background())
	assert.Empty(t, sender.msgs, "no errors → no message")
	assert.Equal(t, 1, cp.sets, "checkpoint still advanced")
}

func TestNotifCycle_NoLockNoWork(t *testing.T) {
	t.Parallel()
	sender := &notifSender{}
	cp := &memCheckpoint{}
	s := newScheduler(notifSettings(true, "*/5 * * * *"), oneNode(),
		notifProm{errs: map[string]float64{"svc/hook": 5}}, sender, fakeLock{ok: false}, cp)

	s.cycle(context.Background())
	assert.Empty(t, sender.msgs)
	assert.Equal(t, 0, cp.sets, "no lock → no checkpoint advance")
}

func TestNotifCycle_SendFailKeepsCheckpoint(t *testing.T) {
	t.Parallel()
	sender := &notifSender{err: assertErr}
	cp := &memCheckpoint{}
	s := newScheduler(notifSettings(true, "*/5 * * * *"), oneNode(),
		notifProm{errs: map[string]float64{"svc/hook": 2}}, sender, fakeLock{ok: true}, cp)

	s.cycle(context.Background())
	assert.Equal(t, 0, cp.sets, "send failed → do not advance checkpoint (retry next tick)")
}

func TestNotifReschedule_RebuildsAndStops(t *testing.T) {
	t.Parallel()
	s := newScheduler(notifSettings(true, "*/5 * * * *"), oneNode(),
		notifProm{}, &notifSender{}, fakeLock{ok: true}, &memCheckpoint{})
	ctx := context.Background()

	s.Reschedule(ctx)
	assert.Equal(t, "*/5 * * * *", s.curExpr)
	require.NotNil(t, s.cron)

	// Смена выражения → пересоздание.
	s.settings = fakeSettingsReader{notifSettings(true, "*/10 * * * *")}
	s.Reschedule(ctx)
	assert.Equal(t, "*/10 * * * *", s.curExpr)

	// Отключение → cron останавливается.
	s.settings = fakeSettingsReader{notifSettings(false, "*/10 * * * *")}
	s.Reschedule(ctx)
	assert.Nil(t, s.cron)
	assert.Empty(t, s.curExpr)
}

func TestNotifReschedule_InvalidCron(t *testing.T) {
	t.Parallel()
	s := newScheduler(notifSettings(true, "not a cron"), oneNode(),
		notifProm{}, &notifSender{}, fakeLock{ok: true}, &memCheckpoint{})
	s.Reschedule(context.Background())
	assert.Nil(t, s.cron, "invalid cron → not armed")
}

func TestFormatErrorMessages_Chunks(t *testing.T) {
	t.Parallel()
	var nodes []nodeErrStat
	for range 300 {
		nodes = append(nodes, nodeErrStat{path: strings.Repeat("p", 20), table: "nexus_default.t", count: 1})
	}
	stats := errStats{total: 300, teams: []teamErrStat{{name: "T", slug: "t", total: 300, nodes: nodes}}}
	msgs := formatErrorMessages(stats, 0, time.Now().UnixMilli())
	require.Greater(t, len(msgs), 1, "long report must be split into multiple messages")
	for _, m := range msgs {
		assert.LessOrEqual(t, len(m), telegramMessageLimit+200)
	}
}

var assertErr = &stubError{"send failed"}

type stubError struct{ s string }

func (e *stubError) Error() string { return e.s }
