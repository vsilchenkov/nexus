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
//
// §114.4: ошибка Metadata или ListGroups и истёкший ctx (бюджет обхода
// usecase'а) — ошибка. Watermarks, OffsetFetch и DescribeGroups по отдельным
// топикам и группам остаются best-effort, но при истёкшем ctx они падают все
// разом, и «успешный» обход вернул бы топики без сообщений и групп.
func (c *Client) Topics(ctx context.Context) ([]port.TopicInfo, error) {
	meta, err := c.kc.Metadata(ctx, &kafka.MetadataRequest{})
	if err != nil {
		return nil, fmt.Errorf("kafka metadata: %w", err)
	}
	online := brokerIDSet(meta.Brokers)
	groupIDs, err := c.listGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	members := c.describeMembers(ctx, groupIDs)

	out := make([]port.TopicInfo, 0, len(meta.Topics))
	for _, t := range meta.Topics {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("kafka topics: %w", err)
		}
		if t.Internal {
			continue
		}
		out = append(out, c.topicInfo(ctx, t, online, groupIDs, members))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("kafka topics: %w", err)
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

// topicConsumerGroups — для каждой группы вычисляет lag по топику
// (kafkapf.PartitionPosition.Lag) с отдельной частью по партициям без commit
// (§114.6). Группа включается, только если имеет хоть один закоммиченный
// offset по этому топику (committed >= 0).
//
// LagTotal — обе части, как их прочитают reader'ы Nexus (§113.4); вычесть
// часть без commit у посторонней группы — дело usecase'а: роль группы
// адаптер не знает.
func (c *Client) topicConsumerGroups(ctx context.Context, topic string, parts []int, wm map[int]kafkapf.Watermark, groupIDs []string, members map[string]int) []port.TopicConsumerGroup {
	var out []port.TopicConsumerGroup
	for _, gid := range groupIDs {
		s := splitGroupLag(parts, wm, c.committedOffsets(ctx, gid, topic, parts))
		if !s.consumes {
			continue
		}
		out = append(out, port.TopicConsumerGroup{
			Group: gid, LagTotal: s.committed + s.uncommitted, Members: members[gid],
			LagUncommitted: s.uncommitted, UncommittedPartitions: s.uncommittedParts,
		})
	}
	return out
}

// GroupLag реализует port.KafkaGroupLagReader (§113.7): lag группы по топику
// из брокера. Частичный ответ (недочитанная партиция) — ошибка: заниженный lag
// на карточке хуже честного «из брокера не прочитано», после которого usecase
// берёт значение из Prometheus.
func (c *Client) GroupLag(ctx context.Context, group, topic string) (int64, error) {
	pos, err := c.offsets.GroupPositions(ctx, group, topic)
	if err != nil {
		return 0, fmt.Errorf("group lag %s/%s: %w", group, topic, err)
	}
	var lag int64
	for _, p := range pos {
		lag += p.Lag()
	}
	return lag, nil
}

// groupLagSplit — lag группы по топику, разделённый по наличию commit (§114.6).
type groupLagSplit struct {
	committed        int64 // по партициям с commit: Last − max(committed, First)
	uncommitted      int64 // по партициям без commit: Last − First
	uncommittedParts int   // партиций без commit (OffsetFetch ответил -1)
	consumes         bool  // есть хоть один commit по топику
}

// splitGroupLag делит lag группы по топику на части с commit и без (§114.6).
//
// Партиция, по которой OffsetFetch не ответил (её нет в committed), не входит
// ни в одну часть: про неё неизвестно ничего. До §114 она считалась
// некоммиченной (-1), и её lag становился размером со весь лог — даже у групп
// Nexus. Партиция без watermarks тоже не даёт lag, но её commit учитывается в
// consumes и в счётчике партиций без commit.
func splitGroupLag(parts []int, wm map[int]kafkapf.Watermark, committed map[int]int64) groupLagSplit {
	var s groupLagSplit
	for _, p := range parts {
		c, ok := committed[p]
		if !ok {
			continue
		}
		if c >= 0 {
			s.consumes = true
		} else {
			s.uncommittedParts++
		}
		w, ok := wm[p]
		if !ok {
			continue
		}
		lag := kafkapf.PartitionPosition{Partition: p, Watermark: w, Committed: c}.Lag()
		if c >= 0 {
			s.committed += lag
		} else {
			s.uncommitted += lag
		}
	}
	return s
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
