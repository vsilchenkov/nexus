package usecase

import (
	"context"
	"sync"

	"nexus/internal/platform/safego"
	"nexus/internal/web/usecase/port"
)

// maxPausedNodesListed — сколько узлов на паузе отдаётся списком под карточкой
// lag (§113.7). Число узлов приходит отдельно (PausedNodesTotal), поэтому
// длинный хвост сводится к «и ещё N», а не раздувает ответ на каждом
// автообновлении экрана.
const maxPausedNodesListed = 20

// KafkaGroupNames — имена consumer-групп Nexus и топиков, которые они читают
// (§113.7). Берутся из конфига Web; пустое имя выключает то, что на нём
// держится: без Main/MainTopic lag берётся только из Prometheus, без
// Paused/PausedTopic строки очереди паузы нет.
type KafkaGroupNames struct {
	Main, Paused, DLQ, Retry string
	MainTopic, PausedTopic   string
}

// role — роль группы Nexus по имени; пусто у посторонней группы.
func (g KafkaGroupNames) role(group string) string {
	// Пустое имя — первым: незаданная в конфиге группа не делает безымянную
	// «основной».
	switch group {
	case "":
		return ""
	case g.Main:
		return port.KafkaGroupRoleMain
	case g.Paused:
		return port.KafkaGroupRolePaused
	case g.DLQ:
		return port.KafkaGroupRoleDLQ
	case g.Retry:
		return port.KafkaGroupRoleRetry
	}
	return ""
}

// KafkaMonitorOption — необязательная зависимость экрана Kafka.
type KafkaMonitorOption func(*KafkaMonitorUsecase)

// WithKafkaGroups задаёт имена групп Nexus (§113.7): по ним размечаются роли
// групп в таблице топиков и читается lag основной и paused-группы.
func WithKafkaGroups(g KafkaGroupNames) KafkaMonitorOption {
	return func(u *KafkaMonitorUsecase) { u.groups = g }
}

// WithKafkaGroupLag подключает чтение lag из брокера (§113.7). Без него
// текущий lag берётся из Prometheus, а очередь паузы не показывается.
func WithKafkaGroupLag(r port.KafkaGroupLagReader) KafkaMonitorOption {
	return func(u *KafkaMonitorUsecase) { u.lagReader = r }
}

// WithPausedNodes подключает список узлов на паузе (§113.7) для строки очереди
// паузы под карточкой lag.
func WithPausedNodes(l port.PausedNodeLister) KafkaMonitorOption {
	return func(u *KafkaMonitorUsecase) { u.pausedNodes = l }
}

// brokerLag — lag группы, прочитанный из брокера; ok=false — не прочитан.
type brokerLag struct {
	value int64
	ok    bool
}

// brokerView — всё, что Overview читает из брокера (§113.7, §114.4). Поля
// пишут разные горутины, каждая свои; читаются после Wait.
type brokerView struct {
	lag      brokerLag // основная группа
	backlog  brokerLag // очередь паузы
	health   port.BrokerHealth
	healthOK bool
	// paused — узлы на паузе; читаются только при непустой очереди паузы.
	paused   []port.NodeKeyRef
	pausedOK bool
}

// readBrokerView выполняет три чтения брокера параллельно друг с другом и с
// withPrometheus (он идёт в текущей горутине) и ждёт все (§114.4). Каждое
// чтение ограничено бюджетом, поэтому Wait не дольше бюджета плюс запрос узлов
// на паузе в PostgreSQL.
func (u *KafkaMonitorUsecase) readBrokerView(ctx context.Context, withPrometheus func()) *brokerView {
	bv := &brokerView{}
	var wg sync.WaitGroup
	wg.Go(func() {
		defer safego.RecoverCtx(ctx, u.logger, "web.kafkaMonitor.mainLag")
		bv.lag = u.groupLag(ctx, u.groups.Main, u.groups.MainTopic)
	})
	wg.Go(func() {
		defer safego.RecoverCtx(ctx, u.logger, "web.kafkaMonitor.pausedBacklog")
		bv.backlog = u.groupLag(ctx, u.groups.Paused, u.groups.PausedTopic)
		if bv.backlog.ok && bv.backlog.value > 0 {
			bv.paused, bv.pausedOK = u.listPausedNodes(ctx, bv.backlog.value)
		}
	})
	wg.Go(func() {
		defer safego.RecoverCtx(ctx, u.logger, "web.kafkaMonitor.brokerHealth")
		if u.admin != nil {
			bv.health, bv.healthOK = readBroker(ctx, u, brokerKeyBrokers, u.budget.Overview, u.admin.BrokerHealth)
		}
	})
	withPrometheus()
	wg.Wait()
	return bv
}

// applyBrokerView переносит прочитанное из брокера в ответ и возвращает
// «брокер не ответил» (§114.5): не удалось хоть одно из настроенных чтений.
//
// Lag основной группы из брокера заменяет Prometheus-значение, уже лежащее в
// Summary.CurrentLag (§113.7). Оно остаётся запасным: запаздывает на интервал
// публикации Sender'а, но живёт, когда брокер не ответил.
func (u *KafkaMonitorUsecase) applyBrokerView(bv *brokerView, res *KafkaOverviewResult) bool {
	mainOn := u.lagConfigured(u.groups.Main, u.groups.MainTopic)
	pausedOn := u.lagConfigured(u.groups.Paused, u.groups.PausedTopic)
	if bv.lag.ok {
		res.Summary.CurrentLag = uint64(bv.lag.value)
		res.LagFromBroker = true
	}
	res.PausedBacklogAvailable = !pausedOn || bv.backlog.ok
	if bv.backlog.ok && bv.backlog.value > 0 {
		res.Summary.PausedBacklog = uint64(bv.backlog.value)
		if bv.pausedOK {
			res.PausedNodesAvailable = true
			res.PausedNodesTotal = len(bv.paused)
			res.PausedNodes = bv.paused[:min(len(bv.paused), maxPausedNodesListed)]
		}
	}
	if bv.healthOK {
		res.BrokerHealth = bv.health
	}
	unreachable := (u.admin != nil && !bv.healthOK) || (mainOn && !bv.lag.ok) || (pausedOn && !bv.backlog.ok)
	res.KafkaAvailable = u.admin != nil && !unreachable
	if unreachable {
		u.logger.Debug("kafka overview: broker reads incomplete",
			u.logger.Str("op", "web.kafkaMonitor.overview"),
			u.logger.Any("health_ok", bv.healthOK), u.logger.Any("main_lag_ok", bv.lag.ok),
			u.logger.Any("paused_backlog_ok", bv.backlog.ok))
	}
	return unreachable
}

// lagConfigured — lag группы читается из брокера: источник подключён, имена
// группы и топика заданы.
func (u *KafkaMonitorUsecase) lagConfigured(group, topic string) bool {
	return u.lagReader != nil && group != "" && topic != ""
}

// groupLag — lag группы по топику из брокера с кешем и бюджетом (§114.4).
// ok=false — значения нет: источник не подключён, имя не задано или брокер не
// ответил (сейчас или в пределах TTL кеша).
func (u *KafkaMonitorUsecase) groupLag(ctx context.Context, group, topic string) brokerLag {
	if !u.lagConfigured(group, topic) {
		u.logger.Debug("kafka broker lag skipped",
			u.logger.Str("op", "web.kafkaMonitor.groupLag"), u.logger.Str("group", group),
			u.logger.Any("reader", u.lagReader != nil))
		return brokerLag{}
	}
	lag, ok := readBroker(ctx, u, brokerKeyLag+group+":"+topic, u.budget.Overview,
		func(c context.Context) (int64, error) { return u.lagReader.GroupLag(c, group, topic) })
	u.logger.Debug("kafka broker lag",
		u.logger.Str("op", "web.kafkaMonitor.groupLag"), u.logger.Str("group", group),
		u.logger.Str("topic", topic), u.logger.Any("lag", lag), u.logger.Any("ok", ok))
	return brokerLag{value: lag, ok: ok}
}

// listPausedNodes — узлы на паузе для строки очереди паузы. Читается только при
// непустой очереди: пустая строку не показывает, и запрос в PostgreSQL на
// каждое автообновление экрана был бы впустую. ok=false при ошибке —
// интерфейс не утверждает «узлов на паузе нет», которого не знает.
func (u *KafkaMonitorUsecase) listPausedNodes(ctx context.Context, backlog int64) ([]port.NodeKeyRef, bool) {
	if u.pausedNodes == nil {
		u.logger.Debug("kafka paused nodes skipped: no lister", u.logger.Str("op", "web.kafkaMonitor.pausedNodes"))
		return nil, false
	}
	nodes, err := u.pausedNodes.PausedNodes(ctx)
	if err != nil {
		u.logger.Warn("kafka paused nodes failed", u.logger.Err(err))
		return nil, false
	}
	if len(nodes) == 0 {
		// Очередь есть, а разбирать её некому: узел удалён или выключен мимо
		// интерфейса, либо сообщения остались от удалённого узла. Это и есть
		// случай алерта NexusPausedBacklogStuck.
		u.logger.Debug("kafka paused backlog without paused nodes",
			u.logger.Str("op", "web.kafkaMonitor.pausedNodes"), u.logger.Any("backlog", backlog))
	}
	return nodes, true
}

// applyGroupRoles проставляет группам топиков роли Nexus (§113.7) и делит lag
// посторонних групп (§114.6). Роль выводится из имени на каждый запрос,
// поэтому кешированные метаданные её не искажают.
//
// Группа Nexus читает партицию без commit с First (§113.4) — её lag настоящий,
// он остаётся в LagTotal, а отдельной плашки «без commit» нет. Посторонней
// группе lag без commit вычитается: откуда она начнёт читать, неизвестно.
// Вычтенная часть обнуляется — повторный вызов на тех же данных ничего не
// меняет (на случай, если копию когда-нибудь перестанут делать).
func (u *KafkaMonitorUsecase) applyGroupRoles(topics []port.TopicInfo) {
	for i := range topics {
		for j := range topics[i].ConsumerGroups {
			g := &topics[i].ConsumerGroups[j]
			g.Role = u.groups.role(g.Group)
			if g.Role != "" {
				g.UncommittedPartitions = 0
				continue
			}
			g.LagTotal -= g.LagUncommitted
			g.LagUncommitted = 0
		}
	}
}
