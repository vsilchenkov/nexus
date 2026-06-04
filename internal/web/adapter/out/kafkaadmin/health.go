package kafkaadmin

import (
	"context"
	"fmt"

	kafka "github.com/segmentio/kafka-go"

	"nexus/internal/web/usecase/port"
)

// partitionOffline — у партиции нет доступного лидера (брокер-лидер не в online
// или физический хост неизвестен). Offline-партиция всегда критична (§6 spec).
func partitionOffline(p kafka.Partition, online map[int]struct{}) bool {
	if p.Leader.Host == "" {
		return true
	}
	_, ok := online[p.Leader.ID]
	return !ok
}

// partitionUnderReplicated — число ISR меньше числа реплик (часть реплик
// отстала/недоступна).
func partitionUnderReplicated(p kafka.Partition) bool {
	return len(p.Isr) < len(p.Replicas)
}

// BrokerHealth агрегирует состояние кластера из Metadata (§4.1 spec):
// online/всего брокеров и счётчики проблемных партиций по всем топикам.
// brokers_total = online ∪ все ID реплик (упавший брокер выпадает из списка
// Brokers, но остаётся в Replicas партиций).
func (c *Client) BrokerHealth(ctx context.Context) (port.BrokerHealth, error) {
	meta, err := c.kc.Metadata(ctx, &kafka.MetadataRequest{})
	if err != nil {
		return port.BrokerHealth{}, fmt.Errorf("kafka metadata: %w", err)
	}
	online := brokerIDSet(meta.Brokers)
	all := make(map[int]struct{}, len(online))
	for id := range online {
		all[id] = struct{}{}
	}

	var underRepl, offline int
	for _, t := range meta.Topics {
		if t.Internal {
			continue
		}
		for _, p := range t.Partitions {
			for _, r := range p.Replicas {
				all[r.ID] = struct{}{}
			}
			if partitionUnderReplicated(p) {
				underRepl++
			}
			if partitionOffline(p, online) {
				offline++
			}
		}
	}
	return port.BrokerHealth{
		BrokersTotal:              len(all),
		BrokersOnline:             len(online),
		UnderReplicatedPartitions: underRepl,
		OfflinePartitions:         offline,
	}, nil
}
