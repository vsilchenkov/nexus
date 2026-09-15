package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/metrics"
)

// ErrNodeAmbiguous — конверт без node_id, а его путь занят узлами нескольких
// команд (§101.5): какой из них принимал сообщение, узнать неоткуда.
var ErrNodeAmbiguous = errors.New("sender: envelope node is ambiguous")

// ReasonNodeAmbiguous — причина в заголовке DLQ и значение метки status у
// неоднозначного конверта (§101.5).
const ReasonNodeAmbiguous = "node_ambiguous"

// resolveEnvelopeNode — узел, который исполняет конверт (§101.5).
//
// Конверт с node_id исполняет ровно тот узел, что его принял, — даже если узел
// с тех пор перенесли в другую команду (NodeUsecase.Move сохраняет id и путь).
// node_id не в каноническом виде UUID трактуется как «узла нет»: PostgreSQL
// ответил бы ошибкой синтаксиса, неотличимой от сбоя БД, и сообщение
// повторялось бы вечно.
//
// Конверт без node_id записан до §101 — ищем по пути: один узел исполняет,
// ни одного — «узла нет», несколько — ErrNodeAmbiguous. До §101 такой конверт
// исполнял случайный из одноимённых узлов (QueryRow без ORDER BY).
func resolveEnvelopeNode(ctx context.Context, nodes NodeReader, logger logging.Logger, env Envelope) (*domain.Node, error) {
	if env.NodeID != "" {
		if !isCanonicalUUID(env.NodeID) {
			logger.Debug("envelope: node_id is not a uuid, node treated as missing",
				logger.Str("id", env.ID),
				logger.Str("node_path", env.NodePath))
			return nil, domain.ErrNodeNotFound
		}
		return nodes.GetByID(ctx, env.NodeID)
	}

	found, err := nodes.ListByPath(ctx, env.NodePath)
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, domain.ErrNodeNotFound
	case 1:
		// §51.9: старый конверт — след того, что очереди после выката §101 ещё
		// не дочищены.
		logger.Debug("envelope: legacy envelope without node_id resolved by path",
			logger.Str("id", env.ID),
			logger.Str("node_path", env.NodePath),
			logger.Str("node_id", found[0].ID))
		return found[0], nil
	default:
		return nil, fmt.Errorf("%w: path %q is shared by %d nodes", ErrNodeAmbiguous, env.NodePath, len(found))
	}
}

// isCanonicalUUID — строка в виде xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx.
// PostgreSQL принимает и другие записи, но Receiver пишет только эту, а
// uuid.Parse сам по себе пропустил бы и форму urn:uuid:…, которую PostgreSQL
// не принимает.
func isCanonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := uuid.Parse(s)
	return err == nil
}

// withNodeIdentity дописывает в заголовки Kafka-сообщения узел конверта
// (§101.4): node_id и team. У конвертов до §101 их нет — и заголовков нет.
func withNodeIdentity(h map[string]string, env Envelope) map[string]string {
	if env.NodeID != "" {
		h["node_id"] = env.NodeID
	}
	if env.Team != "" {
		h["team"] = env.Team
	}
	return h
}

// nodeLabels — метки узла в метриках Sender'а (§101.3): слог и путь из БД.
// Конверт мог быть принят до переноса или переименования узла, но ряд
// принадлежит узлу, который его исполняет.
func nodeLabels(n *domain.Node) metrics.NodeLabels {
	return metrics.NodeLabels{Team: n.TeamSlug, Node: n.Path}
}

// envelopeLabels — метки, когда исполнять конверт некому (узел удалён или
// неоднозначен): слог и путь на момент приёма. У конвертов до §101 слога нет —
// team="".
func envelopeLabels(env Envelope) metrics.NodeLabels {
	return metrics.NodeLabels{Team: env.Team, Node: env.NodePath}
}

// nodeForEnvelope находит узел конверта основного consumer'а. ok=false — узла
// для доставки нет, и res — решение по сообщению.
func (p *AsyncProcessor) nodeForEnvelope(ctx context.Context, raw []byte, env Envelope) (*domain.Node, HandleResult, bool) {
	node, err := resolveEnvelopeNode(ctx, p.nodes, p.logger, env)
	switch {
	case err == nil:
		return node, HandleAck, true
	case errors.Is(err, domain.ErrNodeNotFound):
		p.logger.Warn("async message for unknown node, dropping",
			p.logger.Str("node_path", env.NodePath),
			p.logger.Str("node_id", env.NodeID),
			p.logger.Str("id", env.ID))
		return nil, HandleAck, false
	case errors.Is(err, ErrNodeAmbiguous):
		return nil, p.dlqAmbiguous(ctx, raw, env, err), false
	default:
		p.logger.ErrorWithOp("node read failed", err, "async.Handle",
			p.logger.Str("node_path", env.NodePath))
		return nil, HandleRetry, false
	}
}

// dlqAmbiguous отправляет неоднозначный старый конверт в DLQ вместо доставки
// (§101.5). Доставлять некому: путь занят узлами нескольких команд, а чей это
// конверт, в нём не записано. DLQ, а не тихий ack, — чтобы сообщение осталось
// видно оператору; репроцессор затем сбросит его с учётом в метрике потерь.
func (p *AsyncProcessor) dlqAmbiguous(ctx context.Context, raw []byte, env Envelope, cause error) HandleResult {
	p.logger.Warn("async message is ambiguous: path is shared by several teams, sending to dlq",
		p.logger.Str("op", "async.Handle"),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Str("id", env.ID),
		p.logger.Err(cause))

	hdrs := map[string]string{
		"id":              env.ID,
		"node_path":       env.NodePath,
		"orig_topic":      "nexus.async",
		"reason":          ReasonNodeAmbiguous,
		"last_attempt_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := p.dlq.Produce(ctx, p.dlqTopic, env.NodePath, raw, hdrs); err != nil {
		if errors.Is(err, domain.ErrMessageTooLargeForTopic) {
			// Как в Handle: повтор даст тот же ответ, а offset не двинется —
			// встала бы вся партиция.
			p.logger.ErrorWithOp("dlq publish dropped: message exceeds topic limit", err,
				"async.dlqAmbiguous",
				p.logger.Str("id", env.ID),
				p.logger.Str("node_path", env.NodePath))
			return HandleAck
		}
		p.logger.ErrorWithOp("dlq publish failed", err, "async.dlqAmbiguous",
			p.logger.Str("id", env.ID))
		return HandleRetry
	}
	if p.metrics != nil {
		p.metrics.IncRequest("requestAsync", envelopeLabels(env), ReasonNodeAmbiguous)
	}
	return HandleDLQed
}

// nodeForEnvelope — то же для прохода DLQ (§101.5). Неоднозначный старый
// конверт сбрасывается: доставлять некому, а в DLQ он крутился бы до TTL.
// Сброс учитывается как result="dropped" — его видит алерт потерь DLQ.
func (r *DLQReprocessor) nodeForEnvelope(ctx context.Context, env Envelope) (*domain.Node, ReprocessResult, bool) {
	node, err := resolveEnvelopeNode(ctx, r.nodes, r.logger, env)
	switch {
	case err == nil:
		return node, ReprocessCommit, true
	case errors.Is(err, domain.ErrNodeNotFound):
		r.logger.Info("dlq drop: unknown node",
			r.logger.Str("node_path", env.NodePath),
			r.logger.Str("node_id", env.NodeID),
			r.logger.Str("id", env.ID))
		r.record(envelopeLabels(env), reprocessDropped)
		return nil, ReprocessCommit, false
	case errors.Is(err, ErrNodeAmbiguous):
		r.logger.Warn("dlq drop: envelope is ambiguous, path is shared by several teams",
			r.logger.Str("op", "dlq.reprocess"),
			r.logger.Str("node_path", env.NodePath),
			r.logger.Str("id", env.ID),
			r.logger.Err(err))
		r.record(envelopeLabels(env), reprocessDropped)
		return nil, ReprocessCommit, false
	default:
		// Транзиентная ошибка чтения — не коммитим, попробуем в другой раз.
		r.logger.ErrorWithOp("dlq node read failed", err, "dlq.reprocess",
			r.logger.Str("node_path", env.NodePath))
		return nil, ReprocessRetry, false
	}
}
