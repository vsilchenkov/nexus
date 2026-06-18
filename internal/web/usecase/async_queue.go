package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// ErrAsyncQueueUnavailable — операция управления очередью недоступна (нет Kafka
// peeker / cancel-set, т.е. кластер/Redis не сконфигурированы). Handler → 503.
var ErrAsyncQueueUnavailable = errors.New("async queue management unavailable")

const asyncQueueListLimit = 50 // «первые 50, как в логах» (§34.4)

// AsyncQueueUsecase — управление async-очередью Kafka на узле requestAsync (§34.4).
//
// peeker/cancel опциональны (nil при отсутствии Kafka/Redis): чтение тогда
// деградирует (KafkaAvailable=false), мутации возвращают ErrAsyncQueueUnavailable.
// Узел резолвится по id с проверкой team-scope (чужой узел → 404), как в replay.
type AsyncQueueUsecase struct {
	peeker    port.AsyncQueuePeeker
	cancel    port.QueueCancelWriter
	nodes     port.NodeRepo
	audit     *AuditUsecase
	group     string
	topic     string
	retention time.Duration
	peekCap   int
	logger    logging.Logger
}

func NewAsyncQueueUsecase(
	peeker port.AsyncQueuePeeker,
	cancel port.QueueCancelWriter,
	nodes port.NodeRepo,
	audit *AuditUsecase,
	group, topic string,
	retention time.Duration,
	peekCap int,
	logger logging.Logger,
) *AsyncQueueUsecase {
	if peekCap <= 0 {
		peekCap = 5000
	}
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &AsyncQueueUsecase{
		peeker:    peeker,
		cancel:    cancel,
		nodes:     nodes,
		audit:     audit,
		group:     group,
		topic:     topic,
		retention: retention,
		peekCap:   peekCap,
		logger:    logger,
	}
}

// QueueDepthResult — глубина очереди узла.
type QueueDepthResult struct {
	Count          int64
	Capped         bool
	KafkaAvailable bool
}

// QueueListResult — первые N сообщений очереди узла.
type QueueListResult struct {
	Items          []port.QueueMessageMeta
	Capped         bool
	KafkaAvailable bool
}

// QueuePurgeResult — итог операции очистки.
type QueuePurgeResult struct {
	Cancelled      int
	Capped         bool
	KafkaAvailable bool
}

// resolveNode возвращает узел по id с проверкой team-scope (§34.4 / Phase 10).
func (u *AsyncQueueUsecase) resolveNode(ctx context.Context, nodeID, teamID string) (*domain.Node, error) {
	node, err := u.nodes.Get(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("async queue get node: %w", err)
	}
	if teamID != "" && node.TeamID != teamID {
		return nil, domain.ErrNodeNotFound
	}
	return node, nil
}

// Depth — число неконсюмированных сообщений узла в очереди.
func (u *AsyncQueueUsecase) Depth(ctx context.Context, nodeID, teamID string) (QueueDepthResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueueDepthResult{}, err
	}
	if u.peeker == nil {
		return QueueDepthResult{}, nil
	}
	r, err := u.peeker.PeekDepth(ctx, u.group, u.topic, node.Path, u.peekCap)
	if err != nil {
		u.logger.Warn("async queue depth failed", u.logger.Str("node_path", node.Path), u.logger.Err(err))
		return QueueDepthResult{}, nil
	}
	return QueueDepthResult{Count: r.Count, Capped: r.Capped, KafkaAvailable: true}, nil
}

// List — первые 50 метаданных сообщений узла (без тела).
func (u *AsyncQueueUsecase) List(ctx context.Context, nodeID, teamID string) (QueueListResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueueListResult{}, err
	}
	if u.peeker == nil {
		return QueueListResult{Items: []port.QueueMessageMeta{}}, nil
	}
	r, err := u.peeker.PeekList(ctx, u.group, u.topic, node.Path, asyncQueueListLimit, u.peekCap)
	if err != nil {
		u.logger.Warn("async queue list failed", u.logger.Str("node_path", node.Path), u.logger.Err(err))
		return QueueListResult{Items: []port.QueueMessageMeta{}}, nil
	}
	if r.Items == nil {
		r.Items = []port.QueueMessageMeta{}
	}
	return QueueListResult{Items: r.Items, Capped: r.Capped, KafkaAvailable: true}, nil
}

// Body — тело одного сообщения по (partition, offset) для ленивой подгрузки.
func (u *AsyncQueueUsecase) Body(ctx context.Context, nodeID, teamID string, partition int, offset int64) (port.QueueMessageBody, error) {
	if _, err := u.resolveNode(ctx, nodeID, teamID); err != nil {
		return port.QueueMessageBody{}, err
	}
	if u.peeker == nil {
		return port.QueueMessageBody{}, ErrAsyncQueueUnavailable
	}
	body, err := u.peeker.PeekBody(ctx, u.topic, partition, offset)
	if err != nil {
		return port.QueueMessageBody{}, fmt.Errorf("async queue body: %w", err)
	}
	return body, nil
}

// DeleteOne — отменить одно сообщение очереди (логическое удаление, §34.4).
func (u *AsyncQueueUsecase) DeleteOne(ctx context.Context, actor Actor, nodeID, teamID, msgID string) error {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return err
	}
	if u.cancel == nil {
		return ErrAsyncQueueUnavailable
	}
	if msgID == "" {
		return fmt.Errorf("empty message id")
	}
	n, err := u.cancel.Cancel(ctx, []string{msgID}, u.retention)
	if err != nil {
		return fmt.Errorf("cancel message: %w", err)
	}
	u.audit.Log(ctx, actor, domain.ActionAsyncQueuePurge, "node", node.ID, map[string]any{
		"op": "delete_one", "msg_id": msgID, "cancelled": n,
	})
	return nil
}

// PurgePeriod — отменить сообщения узла с ReceivedAt ∈ [from, to]. Нулевые
// границы = очистить всё (PurgeAll).
func (u *AsyncQueueUsecase) PurgePeriod(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time) (QueuePurgeResult, error) {
	op := "purge_period"
	if from.IsZero() && to.IsZero() {
		op = "purge_all"
	}
	return u.purge(ctx, actor, nodeID, teamID, from, to, op)
}

// PurgeAll — отменить все сообщения узла в очереди.
func (u *AsyncQueueUsecase) PurgeAll(ctx context.Context, actor Actor, nodeID, teamID string) (QueuePurgeResult, error) {
	return u.purge(ctx, actor, nodeID, teamID, time.Time{}, time.Time{}, "purge_all")
}

func (u *AsyncQueueUsecase) purge(ctx context.Context, actor Actor, nodeID, teamID string, from, to time.Time, op string) (QueuePurgeResult, error) {
	node, err := u.resolveNode(ctx, nodeID, teamID)
	if err != nil {
		return QueuePurgeResult{}, err
	}
	if u.peeker == nil || u.cancel == nil {
		return QueuePurgeResult{}, nil
	}
	scan, err := u.peeker.ScanIDs(ctx, u.group, u.topic, node.Path, from, to, u.peekCap)
	if err != nil {
		return QueuePurgeResult{}, fmt.Errorf("async queue scan ids: %w", err)
	}
	cancelled := 0
	if len(scan.IDs) > 0 {
		cancelled, err = u.cancel.Cancel(ctx, scan.IDs, u.retention)
		if err != nil {
			return QueuePurgeResult{}, fmt.Errorf("async queue cancel: %w", err)
		}
	}
	u.audit.Log(ctx, actor, domain.ActionAsyncQueuePurge, "node", node.ID, map[string]any{
		"op": op, "cancelled": cancelled, "capped": scan.Capped,
	})
	return QueuePurgeResult{Cancelled: cancelled, Capped: scan.Capped, KafkaAvailable: true}, nil
}
