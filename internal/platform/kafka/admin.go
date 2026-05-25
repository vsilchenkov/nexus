// Package kafka — обёртки над segmentio/kafka-go: admin для создания
// топиков, producer для Receiver, consumer для Sender. Полный набор
// параметров производительности и устойчивости из §5.3 ТЗ.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	kafka "github.com/segmentio/kafka-go"

	"bus/internal/platform/config"
	"bus/internal/platform/logging"
)

// EnsureTopics создаёт перечисленные топики, если их ещё нет.
// Параметры топика берутся из cfg.Kafka.Topic — единые для всех.
//
// Идемпотентно: уже существующий топик не пересоздаётся, ошибки
// "Topic already exists" глотаются. Любая другая ошибка приводит
// к exit на старте (через MustEnsureTopics в bootstrap).
func EnsureTopics(ctx context.Context, cfg *config.Config, logger logging.Logger, topics ...string) error {
	brokers := splitBrokers(cfg.Kafka.Brokers)
	if len(brokers) == 0 {
		return fmt.Errorf("kafka brokers not configured")
	}

	dialer := &kafka.Dialer{Timeout: 10 * time.Second, DualStack: true}
	conn, err := dialer.DialContext(ctx, "tcp", brokers[0])
	if err != nil {
		return fmt.Errorf("dial kafka %s: %w", brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("get controller: %w", err)
	}
	ctrlAddr := net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port))

	ctrlConn, err := dialer.DialContext(ctx, "tcp", ctrlAddr)
	if err != nil {
		return fmt.Errorf("dial controller %s: %w", ctrlAddr, err)
	}
	defer ctrlConn.Close()

	specs := make([]kafka.TopicConfig, 0, len(topics))
	for _, t := range topics {
		specs = append(specs, kafka.TopicConfig{
			Topic:             t,
			NumPartitions:     cfg.Kafka.Topic.Partitions,
			ReplicationFactor: cfg.Kafka.Topic.ReplicationFactor,
			ConfigEntries: []kafka.ConfigEntry{
				{ConfigName: "retention.ms", ConfigValue: strconv.FormatInt(cfg.Kafka.Topic.RetentionMs, 10)},
				{ConfigName: "retention.bytes", ConfigValue: strconv.FormatInt(cfg.Kafka.Topic.RetentionBytes, 10)},
				{ConfigName: "segment.ms", ConfigValue: strconv.FormatInt(cfg.Kafka.Topic.SegmentMs, 10)},
				{ConfigName: "cleanup.policy", ConfigValue: cfg.Kafka.Topic.CleanupPolicy},
				{ConfigName: "compression.type", ConfigValue: cfg.Kafka.Topic.CompressionType},
				{ConfigName: "max.message.bytes", ConfigValue: strconv.Itoa(cfg.Kafka.Topic.MaxMessageBytes)},
				{ConfigName: "min.insync.replicas", ConfigValue: strconv.Itoa(cfg.Kafka.Topic.MinInsyncReplicas)},
				{ConfigName: "unclean.leader.election.enable", ConfigValue: "false"},
			},
		})
	}

	if err := ctrlConn.CreateTopics(specs...); err != nil {
		// segmentio/kafka-go возвращает kafka.Error 36 (TopicAlreadyExists),
		// его глотаем.
		if !isTopicExists(err) {
			return fmt.Errorf("create topics: %w", err)
		}
	}
	for _, t := range topics {
		logger.Info("kafka topic ready",
			logger.Str("topic", t),
			logger.Int("partitions", cfg.Kafka.Topic.Partitions),
			logger.Int("replication", cfg.Kafka.Topic.ReplicationFactor))
	}
	return nil
}

func splitBrokers(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isTopicExists(err error) bool {
	var ke kafka.Error
	if errors.As(err, &ke) {
		// 36 = TOPIC_ALREADY_EXISTS
		return int(ke) == 36
	}
	if msg := err.Error(); strings.Contains(msg, "Topic with this name already exists") ||
		strings.Contains(msg, "TOPIC_ALREADY_EXISTS") {
		return true
	}
	return false
}
