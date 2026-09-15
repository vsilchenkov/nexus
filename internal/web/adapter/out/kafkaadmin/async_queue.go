package kafkaadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/web/usecase/port"
)

// Управление async-очередью (§34.4): peek поверх транзиентного kafka.Reader без
// GroupID. Читаем от committed-offset группы Sender до high-watermark, отбираем
// сообщения узла по ключу и node_id конверта (§101.8). Ограничено cap'ом
// сообщений (защита от огромной очереди) и bounded-ctx (не висим на запросе).
// Reader не входит в группу Sender, ничего не коммитит и не мешает доставке.

const (
	peekCapDefault = 5000                   // макс. число прочитанных сообщений за peek
	peekTimeout    = 10 * time.Second       // общий бюджет на скан очереди узла
	peekMaxBytes   = 10 << 20               // 10 МБ на fetch
	peekMaxWait    = 500 * time.Millisecond // НЕ дефолтные 10с: после чтения последнего
	// сообщения kafka.Reader в фоне пытается прочитать следующий (ещё пустой) offset и
	// блокируется на MaxWait, а defer r.Close() ждёт этот fetch. С дефолтом 10с peek даже
	// одного сообщения у конца очереди висел ~9с (стенд §35). 500мс держит peek быстрым.
)

var _ port.AsyncQueuePeeker = (*Client)(nil)

// queueEnvelope — минимальная проекция Envelope для чтения из nexus.async.
// Каноническое определение — internal/receiver/usecase/envelope.go; дублируется
// намеренно (как и в sender), чтобы web-адаптер не зависел от receiver/sender.
// JSON-теги обязаны совпадать с каноном.
type queueEnvelope struct {
	ID       string `json:"id"`
	NodePath string `json:"node_path"`
	// NodeID — §101.8: по нему «Очередь» отличает сообщения одноимённых узлов
	// разных команд. Пусто у конвертов, записанных до §101.
	NodeID     string            `json:"node_id,omitempty"`
	Method     string            `json:"method"`
	TargetURL  string            `json:"target_url"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       []byte            `json:"body,omitempty"`
	ReceivedAt time.Time         `json:"received_at"`
}

// decodeQueueMeta декодирует одно сообщение и отбирает сообщения owner (§101.8):
// ключ сообщения — путь узла (ставит продюсер), node_id конверта различает
// одноимённые узлы разных команд. Возвращает (meta, true), если сообщение
// принадлежит узлу и распарсилось; иначе (zero, false).
func decodeQueueMeta(msg kafka.Message, owner port.QueueOwner, topic string) (port.QueueMessageMeta, bool) {
	if string(msg.Key) != owner.Path {
		return port.QueueMessageMeta{}, false // чужой путь отсеивается до разбора JSON
	}
	var env queueEnvelope
	if json.Unmarshal(msg.Value, &env) != nil {
		return port.QueueMessageMeta{}, false
	}
	if !owner.Owns(string(msg.Key), env.NodeID) {
		return port.QueueMessageMeta{}, false
	}
	return port.QueueMessageMeta{
		ID:         env.ID,
		Partition:  msg.Partition,
		Offset:     msg.Offset,
		Topic:      topic,
		Method:     env.Method,
		TargetURL:  env.TargetURL,
		ReceivedAt: env.ReceivedAt,
		BodySize:   len(env.Body),
	}, true
}

// inPeriod — t ∈ [from, to]; нулевые границы трактуются как «без ограничения».
func inPeriod(t, from, to time.Time) bool {
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// PeekList собирает первые limit метаданных сообщений узла.
func (c *Client) PeekList(ctx context.Context, group, topic string, owner port.QueueOwner, limit, capN int) (port.PeekListResult, error) {
	if limit <= 0 {
		limit = 50
	}
	items := make([]port.QueueMessageMeta, 0, limit)
	capped, err := c.scanQueue(ctx, group, topic, owner, capN, func(m port.QueueMessageMeta) bool {
		items = append(items, m)
		return len(items) < limit // дочитали страницу — прекращаем
	})
	if err != nil {
		return port.PeekListResult{}, err
	}
	return port.PeekListResult{Items: items, Capped: capped}, nil
}

// ScanIDs собирает ID сообщений узла с фильтром по периоду (для purge).
func (c *Client) ScanIDs(ctx context.Context, group, topic string, owner port.QueueOwner, from, to time.Time, capN int) (port.ScanIDsResult, error) {
	var ids []string
	capped, err := c.scanQueue(ctx, group, topic, owner, capN, func(m port.QueueMessageMeta) bool {
		if inPeriod(m.ReceivedAt, from, to) {
			ids = append(ids, m.ID)
		}
		return true
	})
	if err != nil {
		return port.ScanIDsResult{}, err
	}
	return port.ScanIDsResult{IDs: ids, Capped: capped}, nil
}

// PeekBody читает тело одного сообщения по (partition, offset), если оно
// принадлежит owner (§101.8). Координату присылает клиент: без сверки владельца
// эндпоинт отдавал тело любого сообщения топика.
func (c *Client) PeekBody(ctx context.Context, topic string, partition int, offset int64, owner port.QueueOwner) (port.QueueMessageBody, error) {
	msg, err := c.readAt(ctx, topic, partition, offset)
	if err != nil {
		return port.QueueMessageBody{}, err
	}
	if string(msg.Key) != owner.Path {
		c.debugNotOwned(topic, partition, offset, owner, "key")
		return port.QueueMessageBody{}, port.ErrQueueMessageNotOwned
	}
	var env queueEnvelope
	if err := json.Unmarshal(msg.Value, &env); err != nil {
		return port.QueueMessageBody{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	if !owner.Owns(string(msg.Key), env.NodeID) {
		c.debugNotOwned(topic, partition, offset, owner, "node_id")
		return port.QueueMessageBody{}, port.ErrQueueMessageNotOwned
	}
	return port.QueueMessageBody{
		ID:        env.ID,
		Method:    env.Method,
		TargetURL: env.TargetURL,
		Headers:   env.Headers,
		Body:      env.Body,
	}, nil
}

// readAt читает одно сообщение по физической координате.
func (c *Client) readAt(ctx context.Context, topic string, partition int, offset int64) (kafka.Message, error) {
	bctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: c.brokers, Topic: topic, Partition: partition,
		MinBytes: 1, MaxBytes: peekMaxBytes, MaxWait: peekMaxWait,
	})
	defer r.Close()
	if err := r.SetOffset(offset); err != nil {
		return kafka.Message{}, fmt.Errorf("set offset: %w", err)
	}
	msg, err := r.ReadMessage(bctx)
	if err != nil {
		return kafka.Message{}, fmt.Errorf("read message: %w", err)
	}
	return msg, nil
}

// debugNotOwned — след отказа в теле чужого сообщения (§51.9): без него жалобу
// «тело не открывается» не отличить от сбоя Kafka. by — что не совпало: ключ
// сообщения или node_id конверта.
func (c *Client) debugNotOwned(topic string, partition int, offset int64, owner port.QueueOwner, by string) {
	c.logger.Debug("kafka peek body refused: message belongs to another node",
		c.logger.Str("op", "web.kafkaAdmin.peekBody"),
		c.logger.Str("topic", topic),
		c.logger.Int("partition", partition),
		c.logger.Any("offset", offset),
		c.logger.Str("node_id", owner.NodeID),
		c.logger.Str("mismatch", by))
}

// scanQueue читает сообщения узла от committed-offset группы до
// high-watermark по всем партициям, вызывая visit для каждого подходящего.
// visit возвращает false → прекратить скан (страница собрана). Возвращает
// capped=true при упоре в cap (прочитано cap сообщений, очередь может быть больше).
func (c *Client) scanQueue(ctx context.Context, group, topic string, owner port.QueueOwner, capN int, visit func(port.QueueMessageMeta) bool) (bool, error) {
	if capN <= 0 {
		capN = peekCapDefault
	}
	t, ok := c.topicMeta(ctx, topic)
	if !ok {
		return false, fmt.Errorf("topic %q not found", topic)
	}
	first, last := c.partitionWatermarks(ctx, t)
	committed := c.committedOffsets(ctx, group, topic, partitionIDs(t))

	scanCtx, cancel := context.WithTimeout(ctx, peekTimeout)
	defer cancel()

	read := 0
	for _, p := range t.Partitions {
		pid := p.ID
		hi, okHi := last[pid]
		if !okHi || hi <= 0 {
			continue
		}
		start := committed[pid]
		if start < 0 {
			start = first[pid] // группа не коммитила эту партицию → от low watermark
		}
		if start >= hi {
			continue // нет неконсюмированных в партиции
		}
		capped, stop := c.scanPartition(scanCtx, topic, pid, start, hi, owner, capN, &read, visit)
		if capped {
			return true, nil
		}
		if stop {
			return false, nil
		}
	}
	return false, nil
}

// scanPartition читает партицию от start до hi-1, отбирая сообщения owner. read —
// общий счётчик прочитанных сообщений (для cap). Возвращает capped (упор в cap)
// и stop (visit попросил прекратить).
func (c *Client) scanPartition(ctx context.Context, topic string, pid int, start, hi int64, owner port.QueueOwner, capN int, read *int, visit func(port.QueueMessageMeta) bool) (capped, stop bool) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: c.brokers, Topic: topic, Partition: pid,
		MinBytes: 1, MaxBytes: peekMaxBytes, MaxWait: peekMaxWait,
	})
	defer r.Close()
	if err := r.SetOffset(start); err != nil {
		c.logger.Warn("kafka peek set offset failed",
			c.logger.Str("op", "web.kafkaAdmin.peek"), c.logger.Str("topic", topic), c.logger.Err(err))
		return false, false
	}
	for {
		if *read >= capN {
			return true, false
		}
		msg, err := r.ReadMessage(ctx)
		if err != nil {
			// ctx done / конец доступного — прекращаем эту партицию (best-effort).
			return false, false
		}
		*read++
		if meta, ok := decodeQueueMeta(msg, owner, topic); ok {
			if !visit(meta) {
				return false, true
			}
		}
		if msg.Offset >= hi-1 {
			return false, false // достигли конца неконсюмированного диапазона
		}
	}
}

// topicMeta возвращает метаданные одного топика из Metadata-запроса.
func (c *Client) topicMeta(ctx context.Context, topic string) (kafka.Topic, bool) {
	meta, err := c.kc.Metadata(ctx, &kafka.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		c.logger.Warn("kafka peek metadata failed",
			c.logger.Str("op", "web.kafkaAdmin.peek"), c.logger.Str("topic", topic), c.logger.Err(err))
		return kafka.Topic{}, false
	}
	for _, t := range meta.Topics {
		if t.Name == topic {
			return t, true
		}
	}
	return kafka.Topic{}, false
}

// committedOffsets — committed offset группы по каждой партиции топика
// (-1, если группа не коммитила). Best-effort: при ошибке — пустая карта (старт
// тогда с low watermark).
func (c *Client) committedOffsets(ctx context.Context, group, topic string, parts []int) map[int]int64 {
	out := map[int]int64{}
	resp, err := c.kc.OffsetFetch(ctx, &kafka.OffsetFetchRequest{GroupID: group, Topics: map[string][]int{topic: parts}})
	if err != nil {
		c.logger.Warn("kafka peek offset fetch failed",
			c.logger.Str("op", "web.kafkaAdmin.peek"), c.logger.Str("group", group), c.logger.Err(err))
		return out
	}
	for _, p := range resp.Topics[topic] {
		out[p.Partition] = p.CommittedOffset
	}
	return out
}
