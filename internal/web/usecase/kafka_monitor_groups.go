package usecase

import (
	"context"

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

// fillBrokerLag (§113.7) заменяет текущий lag значением из брокера по основной
// группе и заполняет очередь паузы. Prometheus-значение, уже лежащее в
// Summary.CurrentLag, остаётся запасным: оно запаздывает на интервал
// публикации Sender'а, но живёт, когда брокер не ответил.
func (u *KafkaMonitorUsecase) fillBrokerLag(ctx context.Context, res *KafkaOverviewResult) {
	if lag, ok := u.groupLag(ctx, u.groups.Main, u.groups.MainTopic); ok {
		res.Summary.CurrentLag = uint64(lag)
		res.LagFromBroker = true
	}
	backlog, ok := u.groupLag(ctx, u.groups.Paused, u.groups.PausedTopic)
	if !ok || backlog == 0 {
		return
	}
	res.Summary.PausedBacklog = uint64(backlog)
	u.fillPausedNodes(ctx, res)
}

// groupLag — lag группы по топику из брокера с кешем (TTL адаптера кеша, как у
// метаданных топиков). ok=false — значения нет: источник не подключён, имя не
// задано или брокер не ответил.
func (u *KafkaMonitorUsecase) groupLag(ctx context.Context, group, topic string) (int64, bool) {
	if u.lagReader == nil || group == "" || topic == "" {
		u.logger.Debug("kafka broker lag skipped",
			u.logger.Str("op", "web.kafkaMonitor.groupLag"), u.logger.Str("group", group),
			u.logger.Any("reader", u.lagReader != nil))
		return 0, false
	}
	key := "lag:" + group + ":" + topic
	if u.cache != nil {
		var cached int64
		if ok, _ := u.cache.Get(ctx, key, &cached); ok {
			return cached, true
		}
	}
	lag, err := u.lagReader.GroupLag(ctx, group, topic)
	if err != nil {
		u.logger.Warn("kafka broker lag failed, falling back to prometheus",
			u.logger.Str("group", group), u.logger.Str("topic", topic), u.logger.Err(err))
		return 0, false
	}
	if u.cache != nil {
		// Best-effort: без кеша следующий запрос просто снова сходит в брокер.
		_ = u.cache.Set(ctx, key, lag)
	}
	u.logger.Debug("kafka broker lag read",
		u.logger.Str("op", "web.kafkaMonitor.groupLag"), u.logger.Str("group", group),
		u.logger.Str("topic", topic), u.logger.Any("lag", lag))
	return lag, true
}

// fillPausedNodes — узлы на паузе для строки очереди паузы. Читается только при
// непустой очереди: пустая строку не показывает, и запрос в PostgreSQL на
// каждое автообновление экрана был бы впустую. При ошибке PausedNodesAvailable
// остаётся false — интерфейс не утверждает «узлов на паузе нет», которого не
// знает.
func (u *KafkaMonitorUsecase) fillPausedNodes(ctx context.Context, res *KafkaOverviewResult) {
	if u.pausedNodes == nil {
		u.logger.Debug("kafka paused nodes skipped: no lister", u.logger.Str("op", "web.kafkaMonitor.pausedNodes"))
		return
	}
	nodes, err := u.pausedNodes.PausedNodes(ctx)
	if err != nil {
		u.logger.Warn("kafka paused nodes failed", u.logger.Err(err))
		return
	}
	res.PausedNodesAvailable = true
	res.PausedNodesTotal = len(nodes)
	res.PausedNodes = nodes[:min(len(nodes), maxPausedNodesListed)]
	if len(nodes) == 0 {
		// Очередь есть, а разбирать её некому: узел удалён или выключен мимо
		// интерфейса, либо сообщения остались от удалённого узла. Это и есть
		// случай алерта NexusPausedBacklogStuck.
		u.logger.Debug("kafka paused backlog without paused nodes",
			u.logger.Str("op", "web.kafkaMonitor.pausedNodes"),
			u.logger.Any("backlog", res.Summary.PausedBacklog))
	}
}

// markGroupRoles проставляет группам топиков роли Nexus (§113.7). Роль
// выводится из имени на каждый запрос, поэтому кешированные метаданные её не
// искажают.
func (u *KafkaMonitorUsecase) markGroupRoles(topics []port.TopicInfo) {
	for i := range topics {
		for j := range topics[i].ConsumerGroups {
			g := &topics[i].ConsumerGroups[j]
			g.Role = u.groups.role(g.Group)
		}
	}
}
