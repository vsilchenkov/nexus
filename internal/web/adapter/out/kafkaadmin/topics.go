package kafkaadmin

import (
	"context"
	"fmt"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/web/usecase/port"
)

// Topics возвращает метаданные всех (не internal) топиков кластера (§4.3 spec):
// партиции, RF, оценку числа сообщений (Σ high-low), consumer-группы с lag,
// состояние реплик. SizeBytes недоступен (см. doc пакета) — 0.
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
	first, last := c.partitionWatermarks(ctx, t)
	for pid, hi := range last {
		if lo, ok := first[pid]; ok && hi > lo {
			ti.MessagesEstimate += hi - lo
		}
	}
	ti.ConsumerGroups = c.topicConsumerGroups(ctx, t.Name, partitionIDs(t), last, groupIDs, members)
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

// partitionWatermarks — first/last offset по каждой партиции топика одним
// ListOffsets-запросом (best-effort: при ошибке — пустые карты).
func (c *Client) partitionWatermarks(ctx context.Context, t kafka.Topic) (first, last map[int]int64) {
	first, last = map[int]int64{}, map[int]int64{}
	reqs := make([]kafka.OffsetRequest, 0, len(t.Partitions)*2)
	for _, p := range t.Partitions {
		reqs = append(reqs, kafka.FirstOffsetOf(p.ID), kafka.LastOffsetOf(p.ID))
	}
	if len(reqs) == 0 {
		return first, last
	}
	resp, err := c.kc.ListOffsets(ctx, &kafka.ListOffsetsRequest{Topics: map[string][]kafka.OffsetRequest{t.Name: reqs}})
	if err != nil {
		c.logger.Warn("kafka list offsets failed",
			c.logger.Str("op", "web.kafkaAdmin.listOffsets"), c.logger.Str("topic", t.Name), c.logger.Err(err))
		return first, last
	}
	for _, po := range resp.Topics[t.Name] {
		first[po.Partition] = po.FirstOffset
		last[po.Partition] = po.LastOffset
	}
	return first, last
}

// topicConsumerGroups — для каждой группы вычисляет суммарный lag по топику
// (Σ max(0, high-committed)). Группа включается, только если имеет хоть один
// закоммиченный offset по этому топику (committed >= 0).
func (c *Client) topicConsumerGroups(ctx context.Context, topic string, parts []int, last map[int]int64, groupIDs []string, members map[string]int) []port.TopicConsumerGroup {
	var out []port.TopicConsumerGroup
	for _, gid := range groupIDs {
		lag, consumes := c.groupLag(ctx, gid, topic, parts, last)
		if !consumes {
			continue
		}
		out = append(out, port.TopicConsumerGroup{Group: gid, LagTotal: lag, Members: members[gid]})
	}
	return out
}

// groupLag — суммарный lag группы по топику и признак потребления топика
// (есть ли committed offset). Best-effort: при ошибке OffsetFetch → (0, false).
func (c *Client) groupLag(ctx context.Context, groupID, topic string, parts []int, last map[int]int64) (int64, bool) {
	resp, err := c.kc.OffsetFetch(ctx, &kafka.OffsetFetchRequest{GroupID: groupID, Topics: map[string][]int{topic: parts}})
	if err != nil {
		c.logger.Warn("kafka offset fetch failed",
			c.logger.Str("op", "web.kafkaAdmin.offsetFetch"), c.logger.Str("group", groupID), c.logger.Err(err))
		return 0, false
	}
	var lag int64
	var consumes bool
	for _, p := range resp.Topics[topic] {
		if p.CommittedOffset < 0 {
			continue
		}
		consumes = true
		if hi, ok := last[p.Partition]; ok && hi > p.CommittedOffset {
			lag += hi - p.CommittedOffset
		}
	}
	return lag, consumes
}
