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

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
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
	logTopicState(ctrlConn, cfg, logger, topics)
	return nil
}

// partitionReader — то, что нужно logTopicState от соединения с брокером.
// Интерфейс на стороне потребителя: тесты подставляют свой источник,
// не поднимая Kafka.
type partitionReader interface {
	ReadPartitions(topics ...string) ([]kafka.Partition, error)
}

// countPartitions сворачивает ответ брокера в «топик → число партиций».
func countPartitions(parts []kafka.Partition) map[string]int {
	counts := make(map[string]int, len(parts))
	for _, p := range parts {
		counts[p.Topic]++
	}
	return counts
}

// logTopicState печатает ФАКТИЧЕСКОЕ состояние топиков после EnsureTopics.
//
// Раньше в лог уходило значение из конфига, а не из брокера, и строка
// «kafka topic ready … partitions=4» читалась как факт. На бою 07.08.2026 это
// был прямой ложный след: партиции уже увеличили до 8 брокерной командой
// (`kafka-topics --alter`), а Sender при каждом старте писал 4 — потому что
// `kafka.topic.partitions` на существующий топик не действует и остался
// прежним в конфиге.
//
// Расхождение факта с конфигом — не ошибка (топик мог быть изменён намеренно),
// но оператор обязан его видеть: от числа партиций зависит, сколько
// consumer-горутин работает (активны min(instances, partitions)).
//
// Ошибку чтения метаданных не эскалируем: топики уже созданы, а старт сервиса
// не должен падать из-за диагностики.
func logTopicState(conn partitionReader, cfg *config.Config, logger logging.Logger, topics []string) {
	parts, err := conn.ReadPartitions(topics...)
	if err != nil {
		logger.Warn("kafka: read partitions failed, reporting configured values",
			logger.Str("op", "kafka.EnsureTopics"), logger.Err(err))
		for _, t := range topics {
			logger.Info("kafka topic ready",
				logger.Str("topic", t),
				logger.Int("partitions_configured", cfg.Kafka.Topic.Partitions),
				logger.Int("replication", cfg.Kafka.Topic.ReplicationFactor))
		}
		return
	}

	counts := countPartitions(parts)
	for _, t := range topics {
		actual := counts[t]
		logger.Info("kafka topic ready",
			logger.Str("topic", t),
			logger.Int("partitions", actual),
			logger.Int("replication", cfg.Kafka.Topic.ReplicationFactor))
		if actual != 0 && actual != cfg.Kafka.Topic.Partitions {
			logger.Warn("kafka topic partitions differ from config",
				logger.Str("op", "kafka.EnsureTopics"),
				logger.Str("topic", t),
				logger.Int("partitions_actual", actual),
				logger.Int("partitions_configured", cfg.Kafka.Topic.Partitions),
				logger.Int("consumer_instances", cfg.Kafka.Consumer.Instances))
		}
	}
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
