package kafkaadmin

import (
	"context"
	"fmt"

	kafka "github.com/segmentio/kafka-go"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/web/usecase/port"
)

// Topics возвращает метаданные всех (не internal) топиков кластера (§4.3 spec):
// партиции, RF, оценку числа сообщений (Σ high-low), consumer-группы с lag,
// состояние реплик. SizeBytes остаётся 0 — размер на диске в админ-протоколе
// недоступен и подмешивается usecase'ом из Prometheus (§75, см. port.TopicInfo).
func (c *Client) Topics(ctx context.Context) ([]port.TopicInfo, error) {
	meta, err := c.kc.Metadata(ctx, &kafka.MetadataRequest{})
	if err != nil {
		return nil, fmt.Errorf("kafka metadata: %w", err)
	}
	online := brokerIDSet(meta.Brokers)
	groupIDs := c.listGroupIDs(ctx)
	members := c.describeMembers(ctx, groupIDs)

	out := make([]port.TopicInfo, 0, len(meta.Topics))
	for _, t := range meta.Topics {
		if t.Internal {
			continue
		}
		out = append(out, c.topicInfo(ctx, t, online, groupIDs, members))
	}
	return out, nil
}

// topicInfo собирает TopicInfo одного топика: счётчики реплик из Metadata,
// watermarks (число сообщений) и lag по группам — отдельными запросами.
func (c *Client) topicInfo(ctx context.Context, t kafka.Topic, online map[int]struct{}, groupIDs []string, members map[string]int) port.TopicInfo {
	ti := port.TopicInfo{
		Name:              t.Name,
		Partitions:        len(t.Partitions),
		ReplicationFactor: replicationFactor(t),
	}
	for _, p := range t.Partitions {
		if partitionUnderReplicated(p) {
			ti.UnderReplicated++
		}
		if partitionOffline(p, online) {
			ti.OfflinePartitions++
		}
	}
	parts := partitionIDs(t)
	wm := c.watermarks(ctx, t.Name, parts)
	for _, w := range wm {
		if w.Last > w.First {
			ti.MessagesEstimate += w.Last - w.First
		}
	}
	ti.ConsumerGroups = c.topicConsumerGroups(ctx, t.Name, parts, wm, groupIDs, members)
	return ti
}

// replicationFactor — RF топика (число реплик первой партиции; одинаков для
// всех партиций в нормальном кластере).
func replicationFactor(t kafka.Topic) int {
	if len(t.Partitions) == 0 {
		return 0
	}
	return len(t.Partitions[0].Replicas)
}

// watermarks — first/last offset партиций топика одним ListOffsets-запросом
// (best-effort: при ошибке — то, что удалось прочитать, и Warn в лог).
func (c *Client) watermarks(ctx context.Context, topic string, parts []int) map[int]kafkapf.Watermark {
	wm, err := c.offsets.Watermarks(ctx, topic, parts)
	if err != nil {
		c.logger.Warn("kafka list offsets failed",
			c.logger.Str("op", "web.kafkaAdmin.listOffsets"), c.logger.Str("topic", topic), c.logger.Err(err))
	}
	return wm
}

// topicConsumerGroups — для каждой группы вычисляет суммарный lag по топику
// (kafkapf.PartitionPosition.Lag). Группа включается, только если имеет хоть
// один закоммиченный offset по этому топику (committed >= 0).
func (c *Client) topicConsumerGroups(ctx context.Context, topic string, parts []int, wm map[int]kafkapf.Watermark, groupIDs []string, members map[string]int) []port.TopicConsumerGroup {
	var out []port.TopicConsumerGroup
	for _, gid := range groupIDs {
		lag, consumes := c.groupLag(ctx, gid, topic, parts, wm)
		if !consumes {
			continue
		}
		out = append(out, port.TopicConsumerGroup{Group: gid, LagTotal: lag, Members: members[gid]})
	}
	return out
}

// groupLag — суммарный lag группы по топику и признак потребления топика
// (есть ли committed offset). Некоммиченная партиция потребляющей группы
// считается от low watermark (§113.4: так её и прочитают reader'ы Nexus; до
// §113 она давала 0 и прятала, например, бэклог в новой партиции). Best-effort:
// при ошибке OffsetFetch без данных → (0, false).
func (c *Client) groupLag(ctx context.Context, groupID, topic string, parts []int, wm map[int]kafkapf.Watermark) (int64, bool) {
	committed := c.committedOffsets(ctx, groupID, topic, parts)
	consumes := false
	for _, off := range committed {
		if off >= 0 {
			consumes = true
			break
		}
	}
	if !consumes {
		return 0, false
	}
	var lag int64
	for _, pos := range kafkapf.JoinPositions(parts, wm, committed) {
		lag += pos.Lag()
	}
	return lag, true
}

// committedOffsets — committed offset группы по каждой партиции топика (-1,
// если группа не коммитила). Best-effort: при ошибке — то, что удалось
// прочитать (возможно, пустая карта), и Warn в лог.
func (c *Client) committedOffsets(ctx context.Context, group, topic string, parts []int) map[int]int64 {
	committed, err := c.offsets.Committed(ctx, group, topic, parts)
	if err != nil {
		c.logger.Warn("kafka offset fetch failed",
			c.logger.Str("op", "web.kafkaAdmin.offsetFetch"), c.logger.Str("group", group),
			c.logger.Str("topic", topic), c.logger.Err(err))
	}
	return committed
}
