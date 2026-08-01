package usecase

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"nexus/internal/domain"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TelegramSender — отправка сообщения в Telegram (реализует platform/telegram.Client).
type TelegramSender interface {
	Send(ctx context.Context, token, chatID, text string) error
}

// DistLock — распределённый лок (реализует redis.NotifLockRedis).
type DistLock interface {
	TryLock(ctx context.Context, ttl time.Duration) (bool, error)
}

// Checkpoint — отметка времени последней проверки в unix-ms (redis.NotifCheckpointRedis).
type Checkpoint interface {
	Get(ctx context.Context) (int64, error)
	Set(ctx context.Context, ms int64) error
}

// SettingsReader — чтение немаскированных настроек (реализует AppSettingsUsecase.Raw).
type SettingsReader interface {
	Raw(ctx context.Context) (*domain.AppSettings, error)
}

// NotificationScheduler по cron-расписанию проверяет ошибки узлов за период и
// шлёт сводку в Telegram, если ошибки есть (§20.3). Расписание берётся из
// app_settings и пересоздаётся при hot-reload (Reschedule). Multi-instance —
// через распределённый лок (§20.4).
type NotificationScheduler struct {
	settings SettingsReader
	teams    port.TeamRepo
	nodes    port.NodeRepo
	prom     port.PromMetrics
	telegram TelegramSender
	lock     DistLock
	state    Checkpoint
	lockTTL  time.Duration
	// instanceID — §70.7: подпись ноды в тексте уведомления. Ноды пишут в один
	// чат, и без подписи непонятно, чьи ошибки пришли.
	instanceID string
	clock      clock.Clock // §4: «сейчас» для окна выборки ошибок
	logger     logging.Logger

	mu      sync.Mutex
	cron    *cron.Cron
	curExpr string
}

func NewNotificationScheduler(
	settings SettingsReader,
	teams port.TeamRepo,
	nodes port.NodeRepo,
	prom port.PromMetrics,
	telegram TelegramSender,
	lock DistLock,
	state Checkpoint,
	instanceID string,
	logger logging.Logger,
	opts ...NotificationOption,
) *NotificationScheduler {
	s := &NotificationScheduler{
		settings: settings, teams: teams, nodes: nodes, prom: prom,
		telegram: telegram, lock: lock, state: state,
		lockTTL: 3 * time.Minute, instanceID: instanceID,
		clock: clock.System(), logger: logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// NotificationOption — функциональная опция конструктора.
type NotificationOption func(*NotificationScheduler)

// WithNotificationClock подменяет источник времени (§4 CLAUDE.md): от него
// зависит верхняя граница окна, за которое собираются ошибки узлов.
func WithNotificationClock(c clock.Clock) NotificationOption {
	return func(s *NotificationScheduler) { s.clock = c }
}

// Run запускает планировщик и блокируется до ctx.Done. Cron-задачи исполняются
// в собственных горутинах robfig/cron.
func (s *NotificationScheduler) Run(ctx context.Context) {
	s.Reschedule(ctx)
	<-ctx.Done()
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}
	s.mu.Unlock()
}

// Reschedule перечитывает расписание из app_settings и пересоздаёт cron при
// изменении выражения. Вызывается из Run и из reload-callback (§20.3).
// robfig/cron не умеет менять spec у entry — только полный rebuild.
func (s *NotificationScheduler) Reschedule(ctx context.Context) {
	raw, err := s.settings.Raw(ctx)
	if err != nil {
		s.logger.ErrorWithOp("notif reschedule: read settings", err, "notif.reschedule")
		return
	}
	tg := raw.Notifications.Telegram
	enabled := tg.Enabled != nil && *tg.Enabled
	expr := ""
	if tg.Cron != nil {
		expr = *tg.Cron
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !enabled || expr == "" {
		s.stopLocked()
		return
	}
	if expr == s.curExpr && s.cron != nil {
		return // расписание не изменилось
	}
	c := cron.New()
	if _, err := c.AddFunc(expr, func() { s.cycle(ctx) }); err != nil {
		s.logger.ErrorWithOp("notif reschedule: bad cron", err, "notif.reschedule",
			s.logger.Str("cron", expr))
		return
	}
	s.stopLocked()
	c.Start()
	s.cron = c
	s.curExpr = expr
	s.logger.Info("notif scheduler armed", s.logger.Str("cron", expr))
}

func (s *NotificationScheduler) stopLocked() {
	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}
	s.curExpr = ""
}

// cycle — один прогон проверки. Берёт лок, считает ошибки за окно
// (last_check, now], шлёт уведомление при наличии ошибок, двигает checkpoint.
func (s *NotificationScheduler) cycle(ctx context.Context) {
	raw, err := s.settings.Raw(ctx)
	if err != nil {
		s.logger.ErrorWithOp("notif cycle: read settings", err, "notif.cycle")
		return
	}
	tg := raw.Notifications.Telegram
	if tg.Enabled == nil || !*tg.Enabled || tg.BotToken == nil || *tg.BotToken == "" ||
		tg.ChatID == nil || *tg.ChatID == "" {
		return
	}

	ok, err := s.lock.TryLock(ctx, s.lockTTL)
	if err != nil {
		s.logger.ErrorWithOp("notif cycle: lock", err, "notif.cycle")
		return
	}
	if !ok {
		return // другая реплика обрабатывает этот тик
	}

	now := s.clock.Now()
	untilMs := now.UnixMilli()
	since, err := s.state.Get(ctx)
	if err != nil {
		s.logger.ErrorWithOp("notif cycle: checkpoint get", err, "notif.cycle")
		return
	}
	if since == 0 {
		since = now.Add(-time.Hour).UnixMilli()
	}

	stats := s.collectErrors(ctx, since, untilMs)
	if stats.total == 0 {
		s.advance(ctx, untilMs)
		return
	}
	for _, msg := range formatErrorMessages(stats, since, untilMs, s.instanceID) {
		if err := s.telegram.Send(ctx, *tg.BotToken, *tg.ChatID, msg); err != nil {
			// Не двигаем checkpoint — ошибки попадут в следующий тик.
			s.logger.ErrorWithOp("notif cycle: telegram send", err, "notif.cycle")
			return
		}
	}
	s.advance(ctx, untilMs)
}

func (s *NotificationScheduler) advance(ctx context.Context, untilMs int64) {
	if err := s.state.Set(ctx, untilMs); err != nil {
		s.logger.ErrorWithOp("notif cycle: checkpoint set", err, "notif.cycle")
	}
}

type nodeErrStat struct {
	path  string
	table string
	count uint64
}

type teamErrStat struct {
	name  string
	slug  string
	total uint64
	nodes []nodeErrStat
}

type errStats struct {
	total uint64
	teams []teamErrStat
}

// collectErrors берёт per-node счётчик «незавершённых» вызовов из Prometheus
// (§22, одна метрика nexus_request_incomplete_total за окно), затем раскладывает
// его по командам, сопоставляя метку node с path узла. Это заменяет N запросов
// CountErrors к ClickHouse одним запросом к тому же источнику, что и графики.
func (s *NotificationScheduler) collectErrors(ctx context.Context, sinceMs, untilMs int64) errStats {
	var stats errStats
	window := time.Duration(untilMs-sinceMs) * time.Millisecond
	if window <= 0 {
		return stats
	}
	byNode, err := s.prom.NodeErrors(ctx, window)
	if err != nil {
		s.logger.ErrorWithOp("notif collect: prometheus node errors", err, "notif.collect")
		return stats
	}
	if len(byNode) == 0 {
		return stats
	}
	teams, err := s.teams.List(ctx)
	if err != nil {
		s.logger.ErrorWithOp("notif collect: list teams", err, "notif.collect")
		return stats
	}
	for _, team := range teams {
		nodes, err := s.nodes.List(ctx, port.ListNodesFilter{TeamID: team.ID})
		if err != nil {
			s.logger.ErrorWithOp("notif collect: list nodes", err, "notif.collect",
				s.logger.Str("team", team.Slug))
			continue
		}
		ts := teamErrStat{name: team.Name, slug: team.Slug}
		for _, n := range nodes {
			cnt := f2u(byNode[n.Path])
			if cnt > 0 {
				ts.nodes = append(ts.nodes, nodeErrStat{path: n.Path, table: n.ClickHouseTable, count: cnt})
				ts.total += cnt
			}
		}
		if ts.total > 0 {
			stats.total += ts.total
			stats.teams = append(stats.teams, ts)
		}
	}
	return stats
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

const telegramMessageLimit = 4000 // запас под 4096

// formatErrorMessages формирует HTML-сообщения, режа по telegramMessageLimit.
//
// instanceID (§70.7) подписывает заголовок: ноды с общим ClickHouse обычно
// шлют алерты в один чат, и без подписи непонятно, чьи узлы сыпят ошибками.
// Пустой идентификатор (нода до §70) заголовок не меняет.
func formatErrorMessages(stats errStats, sinceMs, untilMs int64, instanceID string) []string {
	title := "Nexus: errors detected"
	if instanceID != "" {
		title = fmt.Sprintf("Nexus [%s]: errors detected", htmlEscaper.Replace(instanceID))
	}
	header := fmt.Sprintf("\U0001F6A8 <b>%s</b>\nwindow: %s — %s (UTC)\ntotal errors: %d\n",
		title,
		time.UnixMilli(sinceMs).UTC().Format("2006-01-02 15:04"),
		time.UnixMilli(untilMs).UTC().Format("2006-01-02 15:04"),
		stats.total)

	var (
		out []string
		buf strings.Builder
	)
	buf.WriteString(header)
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	add := func(line string) {
		if buf.Len()+len(line) > telegramMessageLimit {
			flush()
		}
		buf.WriteString(line)
	}
	for _, team := range stats.teams {
		add(fmt.Sprintf("\n<b>%s (%s)</b>: %d\n",
			htmlEscaper.Replace(team.name), htmlEscaper.Replace(team.slug), team.total))
		for _, n := range team.nodes {
			add(fmt.Sprintf("• %s [%s]: %d\n",
				htmlEscaper.Replace(n.path), htmlEscaper.Replace(n.table), n.count))
		}
	}
	flush()
	return out
}
