package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ErrAsyncQueueUnavailable — операция управления очередью недоступна (нет Kafka
// peeker / cancel-set, т.е. кластер/Redis не сконфигурированы). Handler → 503.
var ErrAsyncQueueUnavailable = errors.New("async queue management unavailable")

// ErrAsyncQueueUnknownTopic — запрошен топик вне очереди узла. Handler → 400.
// Allow-list нужен, чтобы через эндпоинт тела сообщения нельзя было прочитать
// произвольный топик кластера.
var ErrAsyncQueueUnknownTopic = errors.New("async queue: unknown topic")

const asyncQueueListLimit = 50 // «первые 50, как в логах» (§34.4)

// AsyncQueueUsecase — управление async-очередью Kafka на узле requestAsync (§34.4).
//
// peeker/cancel опциональны (nil при отсутствии Kafka/Redis): чтение тогда
// деградирует (KafkaAvailable=false), мутации возвращают ErrAsyncQueueUnavailable.
// Узел резолвится по id с проверкой team-scope (чужой узел → 404), как в replay.
type AsyncQueueUsecase struct {
	peeker port.AsyncQueuePeeker
	cancel port.QueueCancelWriter
	failed port.FailedLogsPurger // ClickHouse-логи (очистка неудачных), nil без CH
	nodes  port.NodeRepo
	audit  *AuditUsecase
	group  string
	topic  string
	// pausedGroup/pausedTopic — delay-топик отложенных сообщений paused-узлов
	// (§3.6). Очередь узла физически расщеплена на два топика, и вкладка
	// «Очередь» обязана показывать оба: иначе бэклог паузы «исчезает» из UI, а
	// «Очистить все ожидающие» перестаёт его находить.
	pausedGroup string
	pausedTopic string
	retention   time.Duration
	peekCap     int
	logger      logging.Logger
}

func NewAsyncQueueUsecase(
	peeker port.AsyncQueuePeeker,
	cancel port.QueueCancelWriter,
	failed port.FailedLogsPurger,
	nodes port.NodeRepo,
	audit *AuditUsecase,
	group, topic string,
	pausedGroup, pausedTopic string,
	retention time.Duration,
	peekCap int,
	logger logging.Logger,
) *AsyncQueueUsecase {
	if peekCap <= 0 {
		peekCap = 1000
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &AsyncQueueUsecase{
		peeker:      peeker,
		cancel:      cancel,
		failed:      failed,
		nodes:       nodes,
		audit:       audit,
		group:       group,
		topic:       topic,
		pausedGroup: pausedGroup,
		pausedTopic: pausedTopic,
		retention:   retention,
		peekCap:     peekCap,
		logger:      logger,
	}
}

// queueSource — пара (группа, топик), из которых складывается очередь узла.
type queueSource struct {
	group string
	topic string
}

// sources — все источники очереди: основной топик и delay-топик paused-узлов.
// Второй может быть не сконфигурирован (пустой) — тогда работаем как раньше.
func (u *AsyncQueueUsecase) sources() []queueSource {
	out := []queueSource{{group: u.group, topic: u.topic}}
	if u.pausedTopic != "" {
		out = append(out, queueSource{group: u.pausedGroup, topic: u.pausedTopic})
	}
	return out
}

// QueueListResult — первые N сообщений очереди узла.
type QueueListResult struct {
	Items          []port.QueueMessageMeta
	Capped         bool
	KafkaAvailable bool
	// OriginalWindowHours — §96.7: сколько часов оригинальный конверт живёт в
	// топике, то есть как долго повтор записи узла может отправить ПОЛНОЕ тело,
	// а не журнальную копию. Равен retention топика; интерфейс показывает срок
	// заранее, а не в момент отказа.
	OriginalWindowHours int
}

// QueuePurgeResult — итог операции очистки.
type QueuePurgeResult struct {
	Cancelled      int
	Capped         bool
	KafkaAvailable bool
}

// usesAsyncQueue — есть ли у узла очередь в Kafka. §69.1: вкладка «Очередь»
// открыта для всех типов узлов (у sync там живёт секция «Неудачные доставки»
// из ClickHouse), поэтому Kafka-операции обязаны сами отсекать sync-узлы:
// иначе на каждое открытие вкладки уходил бы бесполезный скан обоих топиков до
// peekCap сообщений. requestAsync и pull-узлы (RabbitMQAsync) идут через один и
// тот же топик nexus.async — очередь есть у обоих.
func usesAsyncQueue(node *domain.Node) bool {
	return node.RootMethod != domain.RootMethodRequest
}

// resolveNode возвращает узел по id с проверкой team-scope (§34.4 / Phase 10).
func (u *AsyncQueueUsecase) resolveNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("async queue get node: %w", err)
	}
	if teamID != "" && node.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	return node, nil
}

// List — первые 50 метаданных сообщений узла (без тела) из ОБОИХ топиков.
//
// Дедуп по id обязателен: сообщение в delay-топике циркулирует (перенос в хвост
// на каждом проходе sweeper'а), поэтому одна и та же запись может встретиться
// под разными offset'ами. Без дедупа список и счётчик «Ожидают отправки»
// раздувались бы кратно числу кругов.
func (u *AsyncQueueUsecase) List(ctx context.Context, nodeID, teamID string) (QueueListResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueueListResult{}, err
	}
	if !usesAsyncQueue(node) {
		u.logger.Debug("async queue list skipped: sync node",
			u.logger.Str("node_path", node.Path),
			u.logger.Str("root_method", string(node.RootMethod)))
		return QueueListResult{
			Items: []port.QueueMessageMeta{}, KafkaAvailable: u.peeker != nil,
			OriginalWindowHours: int(u.retention.Hours()),
		}, nil
	}
	if u.peeker == nil {
		return QueueListResult{Items: []port.QueueMessageMeta{}}, nil
	}

	items := make([]port.QueueMessageMeta, 0, asyncQueueListLimit)
	seen := make(map[string]bool, asyncQueueListLimit)
	capped := false
	for _, src := range u.sources() {
		r, err := u.peeker.PeekList(ctx, src.group, src.topic, node.Path, asyncQueueListLimit, u.peekCap)
		if err != nil {
			// Деградация, а не ошибка: один недоступный топик не должен ронять
			// всю вкладку (Kafka-пик — вспомогательная функция, §34.4).
			u.logger.Warn("async queue list failed",
				u.logger.Str("node_path", node.Path),
				u.logger.Str("topic", src.topic),
				u.logger.Err(err))
			continue
		}
		capped = capped || r.Capped
		for _, it := range r.Items {
			if it.ID != "" && seen[it.ID] {
				continue
			}
			if it.ID != "" {
				seen[it.ID] = true
			}
			items = append(items, it)
		}
	}
	// Приоритет отдаём более старым сообщениям — они уедут первыми.
	slices.SortStableFunc(items, func(a, b port.QueueMessageMeta) int {
		return a.ReceivedAt.Compare(b.ReceivedAt)
	})
	if len(items) > asyncQueueListLimit {
		items = items[:asyncQueueListLimit]
		capped = true
	}
	return QueueListResult{
		Items: items, Capped: capped, KafkaAvailable: true,
		OriginalWindowHours: int(u.retention.Hours()),
	}, nil
}

// Body — тело одного сообщения по (topic, partition, offset) для ленивой
// подгрузки. Пустой topic → основной (обратная совместимость со старым UI).
//
// Для delay-топика координата нестабильна: сообщение переносится в хвост на
// каждом проходе sweeper'а, поэтому захваченный в List offset может устареть.
// Это best-effort — ошибку отдаём наружу, UI просит обновить список.
func (u *AsyncQueueUsecase) Body(ctx context.Context, nodeID, teamID, topic string, partition int, offset int64) (port.QueueMessageBody, error) {
	if _, err := u.resolveNode(ctx, nodeID, teamID); err != nil {
		return port.QueueMessageBody{}, err
	}
	if u.peeker == nil {
		return port.QueueMessageBody{}, ErrAsyncQueueUnavailable
	}
	topic, err := u.resolveTopic(topic)
	if err != nil {
		return port.QueueMessageBody{}, err
	}
	body, err := u.peeker.PeekBody(ctx, topic, partition, offset)
	if err != nil {
		return port.QueueMessageBody{}, fmt.Errorf("async queue body: %w", err)
	}
	return body, nil
}

// resolveTopic валидирует запрошенный топик по allow-list'у: читать произвольный
// топик кластера через этот эндпоинт нельзя.
func (u *AsyncQueueUsecase) resolveTopic(topic string) (string, error) {
	if topic == "" {
		return u.topic, nil
	}
	for _, src := range u.sources() {
		if src.topic == topic {
			return topic, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrAsyncQueueUnknownTopic, topic)
}

// DeleteOne — отменить одно сообщение очереди (логическое удаление, §34.4).
func (u *AsyncQueueUsecase) DeleteOne(ctx context.Context, actor Actor, nodeID, teamID, msgID string) error {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return err
	}
	if u.cancel == nil {
		return ErrAsyncQueueUnavailable
	}
	if msgID == "" {
		return fmt.Errorf("empty message id")
	}
	n, err := u.cancel.Cancel(ctx, []string{msgID}, u.retention)
	if err != nil {
		return fmt.Errorf("cancel message: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionAsyncQueuePurge, "node", node.ID, map[string]any{
		"op": "delete_one", "msg_id": msgID, "cancelled": n,
	})
	return nil
}

// PurgePeriod — отменить сообщения узла с ReceivedAt ∈ [from, to]. Нулевые
// границы = очистить всё (PurgeAll).
func (u *AsyncQueueUsecase) PurgePeriod(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time) (QueuePurgeResult, error) {
	op := "purge_period"
	if from.IsZero() && to.IsZero() {
		op = "purge_all"
	}
	return u.purge(ctx, actor, nodeID, teamID, from, to, op)
}

// PurgeAll — отменить все сообщения узла в очереди.
func (u *AsyncQueueUsecase) PurgeAll(ctx context.Context, actor Actor, nodeID, teamID string) (QueuePurgeResult, error) {
	return u.purge(ctx, actor, nodeID, teamID, time.Time{}, time.Time{}, "purge_all")
}

func (u *AsyncQueueUsecase) purge(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time, op string) (QueuePurgeResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueuePurgeResult{}, err
	}
	if !usesAsyncQueue(node) {
		// §69.1: у sync-узла очереди нет — чистить нечего, скан топиков не нужен.
		u.logger.Debug("async queue purge skipped: sync node",
			u.logger.Str("node_path", node.Path), u.logger.Str("op", op))
		return QueuePurgeResult{}, nil
	}
	if u.peeker == nil || u.cancel == nil {
		return QueuePurgeResult{}, nil
	}
	// Оба топика: бэклог paused-узла (главный юзкейс «очистить очередь мёртвого
	// узла») лежит в delay-топике, а не в основном.
	var ids []string
	seen := make(map[string]bool)
	capped := false
	for _, src := range u.sources() {
		scan, err := u.peeker.ScanIDs(ctx, src.group, src.topic, node.Path, from, to, u.peekCap)
		if err != nil {
			return QueuePurgeResult{}, fmt.Errorf("async queue scan ids (%s): %w", src.topic, err)
		}
		capped = capped || scan.Capped
		for _, id := range scan.IDs {
			if id == "" || seen[id] {
				continue // циркуляция в delay-топике даёт повторы одного id
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	cancelled := 0
	if len(ids) > 0 {
		var err error
		cancelled, err = u.cancel.Cancel(ctx, ids, u.retention)
		if err != nil {
			return QueuePurgeResult{}, fmt.Errorf("async queue cancel: %w", err)
		}
	}
	u.audit.Log(ctx, actor, domain.ActionAsyncQueuePurge, "node", node.ID, map[string]any{
		"op": op, "cancelled": cancelled, "capped": capped,
	})
	return QueuePurgeResult{Cancelled: cancelled, Capped: capped, KafkaAvailable: true}, nil
}

// toMs переводит время в UnixMilli; нулевое время → 0 (без границы окна).
func toMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// purgeFailedMaxBatches — потолок числа проходов одной очистки (§98.4).
//
// Существует не ради производительности, а ради конечности: один клик не должен
// уметь держать запрос неограниченно долго. При батче в peekCap (1000) это
// двести тысяч записей за вызов — больше, чем накапливает узел между разборами.
// Упёрлись — честный Capped, и интерфейс просит нажать ещё раз.
const purgeFailedMaxBatches = 200

// PurgeFailed — очистка «Неудачных доставок» узла за окно [from,to] (нулевые
// границы = всё): (1) отменяет (qcancel) ID этих сообщений, чтобы DLQ-репроцессор
// перестал их повторять (§34.4 tombstone), и (2) удаляет их строки done=0 из
// CH-таблицы узла — чтобы записи исчезли из вида. Без CH (failed==nil) или у узла
// нет таблицы → no-op (нечего чистить). Cancelled в результате — число удалённых
// записей (видимый «очищено N»).
//
// §79.2: оба шага работают с ОДНИМ набором ID. Раньше это были два независимых
// прохода (FailedIDs для tombstone'ов и сплошной DELETE по done=0), из-за чего
// аудит показывал расхождение — боевое cancelled=5 при deleted=10. Инвариант
// сохранён и после §98.4: он держится ВНУТРИ каждого батча.
//
// §98.4: батчи повторяются, пока неудачные не кончатся. До этого очистка
// ограничивалась одним проходом в peekCap записей, а признак Capped интерфейс
// не читал — оператор видел «очищено» и не знал, что осталось ещё сорок шесть
// тысяч. Цикл корректен только потому, что DeleteFailedRows ждёт завершения
// мутации (см. syncMutationCtx): с асинхронным удалением следующая выборка
// возвращала бы те же ID бесконечно.
func (u *AsyncQueueUsecase) PurgeFailed(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time) (QueuePurgeResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueuePurgeResult{}, err
	}
	op := "purge_failed_period"
	if from.IsZero() && to.IsZero() {
		op = "purge_failed_all"
	}
	if u.failed == nil || node.ClickHouseTable == "" {
		return QueuePurgeResult{}, nil
	}
	q := failedQuery(node, toMs(from), toMs(to))

	var (
		totalCancelled int
		totalDeleted   uint64
		batches        int
		capped         bool
	)
	for batches = 1; batches <= purgeFailedMaxBatches; batches++ {
		ids, more, err := u.failed.FailedIDs(ctx, q, u.peekCap)
		if err != nil {
			return QueuePurgeResult{}, fmt.Errorf("async queue failed ids: %w", err)
		}
		if len(ids) == 0 {
			// Кандидаты кончились — либо их и не было, либо все отобранные
			// оказались доставленными позже (FailedIDs отсеивает такие уже
			// после выборки, см. deliveredAmong). Во втором случае за потолком
			// выборки могли остаться настоящие неудачи, но продолжать нельзя:
			// удалять нечего, значит следующая итерация вернула бы ТЕ ЖЕ
			// кандидатов, и цикл стал бы вечным. Поведение то же, что было до
			// §98.4 у одиночного прохода.
			batches--
			break
		}

		// 1) Снимаем повторную доставку: репроцессор дропнет эти ID по tombstone.
		// Для sync-узла шаг пропускаем: DLQ-репроцессора у него нет, tombstone'ы
		// были бы записью в Redis впустую (§69.1).
		if u.cancel != nil && usesAsyncQueue(node) {
			cancelled, cErr := u.cancel.Cancel(ctx, ids, u.retention)
			if cErr != nil {
				return QueuePurgeResult{}, fmt.Errorf("async queue cancel failed: %w", cErr)
			}
			totalCancelled += cancelled
		}

		// 2) Удаляем строки done=0 этих записей из вида «Неудачные доставки».
		deleted, dErr := u.failed.DeleteFailedRows(ctx, q, ids)
		if dErr != nil {
			return QueuePurgeResult{}, fmt.Errorf("async queue delete failed: %w", dErr)
		}
		totalDeleted += deleted

		// §51.9: по этим строкам видно, сколько проходов стоила очистка и на чём
		// она закончилась — иначе жалоба «чистит долго» неразбираема.
		u.logger.Debug("async queue purge failed: batch done",
			u.logger.Str("node_path", node.Path),
			u.logger.Str("op", op),
			u.logger.Int("batch", batches),
			u.logger.Int("ids", len(ids)),
			u.logger.Int("deleted_total", int(totalDeleted)),
			u.logger.Any("more", more))

		if !more {
			break
		}
		if batches == purgeFailedMaxBatches {
			// Бюджет исчерпан, а неудачные ещё есть: сообщаем честно и выходим
			// сами — иначе счётчик проходов в аудите оказался бы на единицу
			// больше, чем проходов реально было.
			capped = true
			break
		}
	}

	if totalDeleted == 0 {
		u.logger.Debug("async queue purge failed: nothing to clean",
			u.logger.Str("node_path", node.Path), u.logger.Str("op", op))
		return QueuePurgeResult{KafkaAvailable: u.cancel != nil}, nil
	}

	u.audit.Log(ctx, actor, domain.ActionAsyncQueuePurge, "node", node.ID, map[string]any{
		"op": op, "cancelled": totalCancelled, "deleted": totalDeleted,
		"batches": batches, "capped": capped,
	})
	return QueuePurgeResult{Cancelled: int(totalDeleted), Capped: capped, KafkaAvailable: u.cancel != nil}, nil
}
