// Package kafkaoffsets — позиции топиков Sender'а из брокера для usecase-слоя
// (§113). Тонкий переходник над kafkapf.Offsets: usecase получает свои типы и
// не знает о kafka-go.
package kafkaoffsets

import (
	"context"
	"time"

	kafkapf "nexus/internal/platform/kafka"
	"nexus/internal/sender/usecase"
)

// PausedTopic — офсеты delay-топика очереди паузы для планировщика прохода
// (§113.3): позиции группы sweeper'а и retention топика.
type PausedTopic struct {
	offsets *kafkapf.Offsets
	group   string
	topic   string
}

var _ usecase.PausedTopicOffsets = (*PausedTopic)(nil)

// NewPausedTopic создаёт переходник для группы group по топику topic.
func NewPausedTopic(offsets *kafkapf.Offsets, group, topic string) *PausedTopic {
	return &PausedTopic{offsets: offsets, group: group, topic: topic}
}

// Positions — позиции группы во всех партициях. Недочитанная партиция —
// ошибка: планировщику нельзя решать о конце раунда по неполным данным.
func (p *PausedTopic) Positions(ctx context.Context) ([]usecase.PausedPartition, error) {
	pos, err := p.offsets.GroupPositions(ctx, p.group, p.topic)
	if err != nil {
		return nil, err
	}
	out := make([]usecase.PausedPartition, 0, len(pos))
	for _, pp := range pos {
		out = append(out, usecase.PausedPartition{
			Partition: pp.Partition, First: pp.First, Last: pp.Last, Committed: pp.Committed,
		})
	}
	return out, nil
}

// Retention — retention.ms топика (0 — без ограничения по времени).
func (p *PausedTopic) Retention(ctx context.Context) (time.Duration, error) {
	return p.offsets.TopicRetention(ctx, p.topic)
}
