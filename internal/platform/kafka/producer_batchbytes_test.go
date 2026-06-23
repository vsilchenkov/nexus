package kafka

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/config"
)

// TestProducerBatchBytes — §42: BatchBytes обязан вмещать одно сообщение размером
// до лимита топика max.message.bytes, иначе segmentio kafka-go отвергает большой
// async-envelope (MessageTooLargeError) ещё до брокера.
func TestProducerBatchBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		batchSize int
		topicMax  int
		want      int64
	}{
		{"topic larger wins (default debug)", 65536, 10 << 20, 10 << 20},
		{"batch larger wins", 20 << 20, 10 << 20, 20 << 20},
		{"equal", 1 << 20, 1 << 20, 1 << 20},
		{"topic unset → batch", 1 << 20, 0, 1 << 20},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			cfg.Kafka.Producer.BatchSize = tc.batchSize
			cfg.Kafka.Topic.MaxMessageBytes = tc.topicMax
			assert.Equal(t, tc.want, producerBatchBytes(cfg))
		})
	}
}
