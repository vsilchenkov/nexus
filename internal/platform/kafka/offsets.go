package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

// offsetsAPI — то, что Offsets использует от *kafka.Client. Интерфейс на
// стороне потребителя: юниты подставляют фейк без брокера.
type offsetsAPI interface {
	Metadata(ctx context.Context, req *kafka.MetadataRequest) (*kafka.MetadataResponse, error)
	ListOffsets(ctx context.Context, req *kafka.ListOffsetsRequest) (*kafka.ListOffsetsResponse, error)
	OffsetFetch(ctx context.Context, req *kafka.OffsetFetchRequest) (*kafka.OffsetFetchResponse, error)
	DescribeConfigs(ctx context.Context, req *kafka.DescribeConfigsRequest) (*kafka.DescribeConfigsResponse, error)
	DescribeGroups(ctx context.Context, req *kafka.DescribeGroupsRequest) (*kafka.DescribeGroupsResponse, error)
}

// Offsets читает позиции топиков и consumer-групп прямо из брокера (§113.4):
// watermarks (ListOffsets), committed-офсеты группы (OffsetFetch), retention
// топика (DescribeConfigs). Общий источник для Sender (lag, планировщик прохода
// очереди паузы) и Web (экран Kafka, peek очереди).
//
// Почему не kafka.Reader.Stats: Lag там — разница между HWM последнего пакета
// и позицией предвыборки конкретного reader'а, без номера партиции. Он даёт пилу
// при перезаписи топика и не растёт у зависшего reader'а (он же ничего не
// выбирает). Брокер знает и конец лога, и committed-офсет группы — это и есть
// честный lag, одинаковый для всех реплик.
//
// Ошибки отдельных партиций не прячутся: методы возвращают то, что удалось
// прочитать, ВМЕСТЕ с errors.Join ошибок партиций. Вызывающий решает сам —
// экран мониторинга показывает частичные данные, планировщик считает их
// неизвестными.
type Offsets struct {
	api offsetsAPI
}

// NewOffsets создаёт читателя офсетов поверх готового клиента (Web переиспользует
// клиент своего admin-адаптера).
func NewOffsets(kc *kafka.Client) *Offsets {
	return &Offsets{api: kc}
}

// NewOffsetsForBrokers создаёт читателя офсетов со своим клиентом к брокерам
// (CSV host:port). timeout ≤ 0 → 5 с.
func NewOffsetsForBrokers(brokers string, timeout time.Duration) *Offsets {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return NewOffsets(&kafka.Client{Addr: kafka.TCP(splitBrokers(brokers)...), Timeout: timeout})
}

// Watermark — границы партиции: First — самый ранний доступный offset (low
// watermark, сдвигается retention'ом), Last — offset, который получит следующее
// записанное сообщение (high watermark).
type Watermark struct {
	First, Last int64
}

// PartitionPosition — позиция группы в одной партиции.
type PartitionPosition struct {
	Partition int
	Watermark
	// Committed — committed-офсет группы; -1 — группа партицию не коммитила.
	Committed int64
}

// Lag — сколько сообщений партиции группе осталось прочитать. Позиция чтения —
// committed, но не раньше First: всё, что левее, удалено retention'ом и прочитано
// не будет. Некоммиченная партиция (-1) читается с First — так настроены все
// reader'ы Nexus (StartOffset = FirstOffset).
func (p PartitionPosition) Lag() int64 {
	return max(0, p.Last-max(p.Committed, p.First))
}

// ErrTopicNotFound — топика нет в кластере: брокер (или кеш метаданных
// kafka-go) ответил, что такого топика не знает. Отличает «ждать в топике
// нечему» от «брокер не ответил» (ревизия §114).
var ErrTopicNotFound = errors.New("kafka: topic not found")

// Partitions возвращает номера партиций топика из Metadata. Топика нет —
// ошибка с ErrTopicNotFound (и исходной ошибкой kafka-go, если она была).
func (o *Offsets) Partitions(ctx context.Context, topic string) ([]int, error) {
	meta, err := o.api.Metadata(ctx, &kafka.MetadataRequest{Topics: []string{topic}})
	if err != nil {
		return nil, fmt.Errorf("kafka metadata %s: %w", topic, err)
	}
	for _, t := range meta.Topics {
		if t.Name != topic {
			continue
		}
		if errors.Is(t.Error, kafka.UnknownTopicOrPartition) {
			return nil, fmt.Errorf("kafka metadata %s: %w: %w", topic, ErrTopicNotFound, t.Error)
		}
		if t.Error != nil {
			return nil, fmt.Errorf("kafka metadata %s: %w", topic, t.Error)
		}
		ids := make([]int, 0, len(t.Partitions))
		for _, p := range t.Partitions {
			ids = append(ids, p.ID)
		}
		return ids, nil
	}
	return nil, fmt.Errorf("kafka metadata %s: %w", topic, ErrTopicNotFound)
}

// Watermarks — first/last offset каждой из parts одним ListOffsets-запросом.
// Партиции с ошибкой в карту не попадают, их ошибки возвращаются вместе с картой.
func (o *Offsets) Watermarks(ctx context.Context, topic string, parts []int) (map[int]Watermark, error) {
	out := make(map[int]Watermark, len(parts))
	if len(parts) == 0 {
		return out, nil
	}
	reqs := make([]kafka.OffsetRequest, 0, len(parts)*2)
	for _, p := range parts {
		reqs = append(reqs, kafka.FirstOffsetOf(p), kafka.LastOffsetOf(p))
	}
	resp, err := o.api.ListOffsets(ctx, &kafka.ListOffsetsRequest{Topics: map[string][]kafka.OffsetRequest{topic: reqs}})
	if err != nil {
		return out, fmt.Errorf("kafka list offsets %s: %w", topic, err)
	}
	var errs []error
	for _, po := range resp.Topics[topic] {
		if po.Error != nil {
			errs = append(errs, fmt.Errorf("partition %d: %w", po.Partition, po.Error))
			continue
		}
		out[po.Partition] = Watermark{First: po.FirstOffset, Last: po.LastOffset}
	}
	return out, wrapPartitionErrs("kafka list offsets "+topic, errs)
}

// Committed — committed-офсет группы по каждой из parts (-1 — не коммитила).
// Ошибка группы целиком (координатор не готов) — ошибка без карты; ошибки
// отдельных партиций — вместе с картой.
func (o *Offsets) Committed(ctx context.Context, group, topic string, parts []int) (map[int]int64, error) {
	out := make(map[int]int64, len(parts))
	resp, err := o.api.OffsetFetch(ctx, &kafka.OffsetFetchRequest{GroupID: group, Topics: map[string][]int{topic: parts}})
	if err != nil {
		return out, fmt.Errorf("kafka offset fetch %s/%s: %w", group, topic, err)
	}
	if resp.Error != nil {
		return out, fmt.Errorf("kafka offset fetch %s/%s: %w", group, topic, resp.Error)
	}
	var errs []error
	for _, p := range resp.Topics[topic] {
		if p.Error != nil {
			errs = append(errs, fmt.Errorf("partition %d: %w", p.Partition, p.Error))
			continue
		}
		out[p.Partition] = p.CommittedOffset
	}
	return out, wrapPartitionErrs("kafka offset fetch "+group+"/"+topic, errs)
}

// GroupPositions — позиция группы во всех партициях топика: Metadata +
// ListOffsets + OffsetFetch. Партиция попадает в ответ, только если известны и
// её watermarks, и committed; любая недочитанная — ошибка рядом с ответом.
// Партиция, которой брокер без ошибки не вернул в OffsetFetch, считается
// некоммиченной (-1).
func (o *Offsets) GroupPositions(ctx context.Context, group, topic string) ([]PartitionPosition, error) {
	parts, err := o.Partitions(ctx, topic)
	if err != nil {
		return nil, err
	}
	wm, wmErr := o.Watermarks(ctx, topic, parts)
	committed, cErr := o.Committed(ctx, group, topic, parts)
	pos := JoinPositions(parts, wm, committed)
	if cErr != nil {
		// Отсутствие в карте при ошибке OffsetFetch — «не прочитано», а не «не
		// коммитила»: с -1 lag партиции стал бы размером со весь лог (координатор
		// группы грузится после рестарта брокера, а ListOffsets уже отвечает).
		pos = slices.DeleteFunc(pos, func(p PartitionPosition) bool {
			_, ok := committed[p.Partition]
			return !ok
		})
	}
	return pos, errors.Join(wmErr, cErr)
}

// JoinPositions сводит watermarks и committed-офсеты в позиции по партициям (в
// порядке parts). Партиция без watermarks пропускается; без committed-офсета —
// считается некоммиченной (-1).
func JoinPositions(parts []int, wm map[int]Watermark, committed map[int]int64) []PartitionPosition {
	out := make([]PartitionPosition, 0, len(parts))
	for _, p := range parts {
		w, ok := wm[p]
		if !ok {
			continue
		}
		c, ok := committed[p]
		if !ok {
			c = -1
		}
		out = append(out, PartitionPosition{Partition: p, Watermark: w, Committed: c})
	}
	return out
}

// TopicRetention — retention.ms топика из брокера (DescribeConfigs). 0 —
// хранение без ограничения по времени (retention.ms = -1).
func (o *Offsets) TopicRetention(ctx context.Context, topic string) (time.Duration, error) {
	resp, err := o.api.DescribeConfigs(ctx, &kafka.DescribeConfigsRequest{
		Resources: []kafka.DescribeConfigRequestResource{{
			ResourceType: kafka.ResourceTypeTopic,
			ResourceName: topic,
			ConfigNames:  []string{"retention.ms"},
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("kafka describe configs %s: %w", topic, err)
	}
	for _, r := range resp.Resources {
		if r.ResourceName != topic {
			continue
		}
		if r.Error != nil {
			return 0, fmt.Errorf("kafka describe configs %s: %w", topic, r.Error)
		}
		return retentionFromEntries(topic, r.ConfigEntries)
	}
	return 0, fmt.Errorf("kafka describe configs %s: topic not in response", topic)
}

// GroupAssignment возвращает партиции топика, назначенные участникам группы,
// по client.id участника (§113.6). Сторож зависшего consumer'а сопоставляет
// по нему партиции со своими reader'ами: client.id задаёт WithClientID. Группа
// в ребалансе отдаёт участников без назначения — их в карте нет.
func (o *Offsets) GroupAssignment(ctx context.Context, group, topic string) (map[string][]int, error) {
	resp, err := o.api.DescribeGroups(ctx, &kafka.DescribeGroupsRequest{GroupIDs: []string{group}})
	if err != nil {
		return nil, fmt.Errorf("kafka describe group %s: %w", group, err)
	}
	out := map[string][]int{}
	for _, g := range resp.Groups {
		if g.GroupID != group {
			continue
		}
		if g.Error != nil {
			return nil, fmt.Errorf("kafka describe group %s: %w", group, g.Error)
		}
		for _, m := range g.Members {
			for _, t := range m.MemberAssignments.Topics {
				if t.Topic == topic && len(t.Partitions) > 0 {
					out[m.ClientID] = append(out[m.ClientID], t.Partitions...)
				}
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("kafka describe group %s: group not in response", group)
}

// retentionFromEntries находит retention.ms среди записей конфига топика.
func retentionFromEntries(topic string, entries []kafka.DescribeConfigResponseConfigEntry) (time.Duration, error) {
	for _, e := range entries {
		if e.ConfigName != "retention.ms" {
			continue
		}
		ms, err := strconv.ParseInt(e.ConfigValue, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("kafka retention.ms of %s: %w", topic, err)
		}
		if ms < 0 {
			return 0, nil
		}
		return time.Duration(ms) * time.Millisecond, nil
	}
	return 0, fmt.Errorf("kafka retention.ms of %s: not reported", topic)
}

// wrapPartitionErrs сворачивает ошибки партиций в одну с префиксом операции;
// пустой список — nil.
func wrapPartitionErrs(op string, errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", op, errors.Join(errs...))
}
