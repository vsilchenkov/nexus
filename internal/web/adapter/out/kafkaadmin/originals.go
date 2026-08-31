package kafkaadmin

import (
	"context"
	"encoding/json"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/web/usecase/port"
)

// Поиск оригинальных конвертов в топиках очереди (§96). От peek экрана очереди
// (§34.4) отличается тремя вещами: ищем ПО НАБОРУ ID, а не по всему узлу;
// умеем стартовать не только от committed offset группы, но и от offset по
// времени (конверт с истёкшим dlq_ttl лежит ПОЗАДИ committed offset
// репроцессора, оставаясь живым до retention топика); прекращаем скан, как
// только набор собран.
//
// Всё best-effort: недоступный брокер, отсутствующий топик, упор в cap — это
// «конверт не найден», а не ошибка. Решение об отказе принимает домен (§96.4).

var _ port.AsyncOriginalReader = (*Client)(nil)

// envelopeID — облегчённая проекция конверта для ProbeOriginals: тело записи
// может быть в мегабайты, а предпросмотру §85 нужен только факт наличия.
type envelopeID struct {
	ID         string    `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
}

// FindOriginals возвращает найденные конверты с полными телами.
func (c *Client) FindOriginals(ctx context.Context, in port.OriginalLookup) (map[string]port.AsyncOriginal, error) {
	out := make(map[string]port.AsyncOriginal, len(in.IDs))
	c.lookupOriginals(ctx, in, true, func(id string, env *queueEnvelope, topic string) {
		out[id] = port.AsyncOriginal{
			ID:         id,
			Body:       env.Body,
			Headers:    env.Headers,
			Topic:      topic,
			ReceivedAt: env.ReceivedAt,
		}
	})
	return out, nil
}

// ProbeOriginals возвращает только признак наличия конверта, без чтения тел.
func (c *Client) ProbeOriginals(ctx context.Context, in port.OriginalLookup) (map[string]bool, error) {
	out := make(map[string]bool, len(in.IDs))
	c.lookupOriginals(ctx, in, false, func(id string, _ *queueEnvelope, _ string) {
		out[id] = true
	})
	return out, nil
}

// lookupOriginals обходит источники в заданном порядке и зовёт hit на каждый
// найденный конверт. Найденные идентификаторы выбывают из набора: как только он
// пуст, скан прекращается — обычный случай (одна запись) читает несколько
// сообщений хвоста DLQ и заканчивается.
func (c *Client) lookupOriginals(ctx context.Context, in port.OriginalLookup, withBody bool, hit func(string, *queueEnvelope, string)) {
	if len(in.IDs) == 0 || len(in.Sources) == 0 || in.NodePath == "" {
		return
	}
	want := make(map[string]struct{}, len(in.IDs))
	for _, id := range in.IDs {
		want[id] = struct{}{}
	}
	capN := in.Cap
	if capN <= 0 {
		capN = peekCapDefault
	}
	scanCtx, cancel := context.WithTimeout(ctx, peekTimeout)
	defer cancel()

	read := 0
	for _, src := range in.Sources {
		if len(want) == 0 || read >= capN {
			break
		}
		// Живой хвост источника: то, что consumer-group ещё не разобрала.
		c.scanTopicForIDs(scanCtx, src.Topic, scanOrigin{group: src.Group}, in.NodePath, want, &read, capN, withBody, hit)
		// Глубокий проход — только там, где он осмыслен (DLQ) и только когда
		// известна нижняя граница по времени: скан топика «с начала» тянул бы
		// историю всех узлов инсталляции.
		if src.Deep && len(want) > 0 && read < capN && !in.Since.IsZero() {
			c.scanTopicForIDs(scanCtx, src.Topic, scanOrigin{since: in.Since}, in.NodePath, want, &read, capN, withBody, hit)
		}
	}
	c.logger.Debug("kafka originals lookup done",
		c.logger.Str("op", "web.kafkaAdmin.originals"),
		c.logger.Str("node_path", in.NodePath),
		c.logger.Int("requested", len(in.IDs)),
		c.logger.Int("missing", len(want)),
		c.logger.Int("read", read),
		c.logger.Any("capped", read >= capN))
}

// scanOrigin — откуда начинать чтение партиции: от committed offset группы
// (group непусто) либо от offset по времени (since).
type scanOrigin struct {
	group string
	since time.Time
}

// scanTopicForIDs читает партиции топика, пока набор не собран или не исчерпан cap.
func (c *Client) scanTopicForIDs(
	ctx context.Context, topic string, origin scanOrigin, nodePath string,
	want map[string]struct{}, read *int, capN int, withBody bool, hit func(string, *queueEnvelope, string),
) {
	t, ok := c.topicMeta(ctx, topic)
	if !ok {
		return // топика нет (не сконфигурирован/не создан) — не ошибка поиска
	}
	first, last := c.partitionWatermarks(ctx, t)
	var committed map[int]int64
	if origin.group != "" {
		committed = c.committedOffsets(ctx, origin.group, topic, partitionIDs(t))
	}
	for _, p := range t.Partitions {
		if len(want) == 0 || *read >= capN {
			return
		}
		hi, okHi := last[p.ID]
		if !okHi || hi <= 0 {
			continue
		}
		start := int64(-1) // -1 → позиционирование по времени
		if origin.group != "" {
			start = committed[p.ID]
			if start < 0 {
				start = first[p.ID] // группа не коммитила эту партицию
			}
			if start >= hi {
				continue // живой хвост пуст
			}
		}
		c.readPartitionForIDs(ctx, topic, p.ID, start, hi, origin.since, nodePath, want, read, capN, withBody, hit)
	}
}

// readPartitionForIDs читает одну партицию от start (или от времени since, если
// start < 0) до hi-1, отбирая сообщения узла с нужными идентификаторами.
func (c *Client) readPartitionForIDs(
	ctx context.Context, topic string, pid int, start, hi int64, since time.Time, nodePath string,
	want map[string]struct{}, read *int, capN int, withBody bool, hit func(string, *queueEnvelope, string),
) {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: c.brokers, Topic: topic, Partition: pid,
		MinBytes: 1, MaxBytes: peekMaxBytes, MaxWait: peekMaxWait,
	})
	defer r.Close()
	if err := seekReader(ctx, r, start, since); err != nil {
		c.logger.Debug("kafka originals seek failed",
			c.logger.Str("op", "web.kafkaAdmin.originals"),
			c.logger.Str("topic", topic), c.logger.Int("partition", pid), c.logger.Err(err))
		return
	}
	for {
		if *read >= capN || len(want) == 0 {
			return
		}
		msg, err := r.ReadMessage(ctx)
		if err != nil {
			return // ctx done / конец доступного — best-effort, как у peek
		}
		*read++
		if string(msg.Key) == nodePath {
			c.matchEnvelope(msg, topic, want, withBody, hit)
		}
		if msg.Offset >= hi-1 {
			return
		}
	}
}

// matchEnvelope разбирает сообщение узла и, если его ID в наборе, отдаёт конверт
// вызывающей стороне. Разбор с телом делается ТОЛЬКО когда тело действительно
// нужно (FindOriginals): у ProbeOriginals окно может быть в тысячи записей.
func (c *Client) matchEnvelope(
	msg kafka.Message, topic string, want map[string]struct{}, withBody bool, hit func(string, *queueEnvelope, string),
) {
	if !withBody {
		var idOnly envelopeID
		if json.Unmarshal(msg.Value, &idOnly) != nil {
			return
		}
		if _, ok := want[idOnly.ID]; !ok {
			return
		}
		delete(want, idOnly.ID)
		hit(idOnly.ID, nil, topic)
		return
	}
	var env queueEnvelope
	if json.Unmarshal(msg.Value, &env) != nil {
		return
	}
	if _, ok := want[env.ID]; !ok {
		return
	}
	delete(want, env.ID)
	hit(env.ID, &env, topic)
}

// seekReader ставит читателя на стартовую позицию: явный offset либо ближайший
// к моменту времени.
func seekReader(ctx context.Context, r *kafka.Reader, start int64, since time.Time) error {
	if start >= 0 {
		return r.SetOffset(start)
	}
	return r.SetOffsetAt(ctx, since)
}
