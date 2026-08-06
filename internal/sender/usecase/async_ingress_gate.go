package usecase

import (
	"context"
	"fmt"
	"time"

	"nexus/internal/domain"
)

// ReasonNodeNotAsync — маркер в DLQ и в журнале: сообщение не доставлено,
// потому что узел больше не принимает async (§83.7). Отличается от обычной
// ошибки доставки тем, что приёмник не вызывался вовсе.
const ReasonNodeNotAsync = "node_not_async"

// asyncIngressRevoked решает, потерял ли узел право на доставку уже принятого
// async-сообщения (§83.7).
//
// Задача, которую он решает: оператор перевёл узел обратно в sync, а в очереди
// осталось накопленное. Доставлять его нельзя — клиент об этом не знает и
// повторит те же данные сам, получив дубли у приёмника.
//
// Ключевая тонкость: у sync-узла в очереди законно лежат ДРУГИЕ сообщения —
// те, что попали туда по §3.6, пока узел был на паузе. Их доставлять
// обязательно. Различает их Envelope.IngressMethod — режим узла В МОМЕНТ
// ПРИЁМА, а не сейчас.
//
// Сообщения без IngressMethod (приняты до §83) доставляются как раньше:
// на выкате очередь не должна обнулиться из-за нового поля.
func asyncIngressRevoked(node *domain.Node, env Envelope) (reason string, stop bool) {
	if env.IngressMethod != string(domain.RootMethodRequestAsync) {
		return "", false
	}
	if node.RootMethod == domain.RootMethodRequestAsync {
		return "", false
	}
	return fmt.Sprintf("%s root_method=%s", ReasonNodeNotAsync, node.RootMethod), true
}

// dropNotAsync отправляет сообщение в DLQ вместо доставки.
//
// Почему DLQ, а не тихий ack: ничего не теряется, запись видна в «Очереди» и
// «Неудачных доставках», а при возврате узла в async в пределах dlq_ttl_seconds
// репроцессор доставит её сам. Тихий отброс потребовал бы ручного «Повторить
// все» (§36.11), а тот идёт через HTTP на Receiver и упёрся бы в гейт §82.3,
// пока узел sync, — то есть восстановление было бы недоступно ровно тогда,
// когда оно нужно.
func (p *AsyncProcessor) dropNotAsync(ctx context.Context, raw []byte, env Envelope, reason string) HandleResult {
	// Warn, а не info: это рассинхронизация настроек и клиента, её надо чинить.
	p.logger.Warn("async delivery revoked: node is no longer async",
		p.logger.Str("op", "async.Handle"),
		p.logger.Str("node_path", env.NodePath),
		p.logger.Str("id", env.ID),
		p.logger.Str("reason", reason))

	hdrs := map[string]string{
		"id":         env.ID,
		"node_path":  env.NodePath,
		"orig_topic": "nexus.async",
		// В журнале ClickHouse тип пишется константой requestAsync
		// (см. buildSendInput), поэтому реальный root_method обязан быть
		// здесь: иначе отбой по гейту неотличим от обычной ошибки доставки.
		"reason":          reason,
		"last_attempt_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := p.dlq.Produce(ctx, p.dlqTopic, env.NodePath, raw, hdrs); err != nil {
		p.logger.ErrorWithOp("dlq publish failed", err, "async.dropNotAsync",
			p.logger.Str("id", env.ID))
		return HandleRetry
	}
	if p.metrics != nil {
		p.metrics.RequestsTotal.
			WithLabelValues("requestAsync", env.NodePath, ReasonNodeNotAsync).Inc()
	}
	return HandleDLQed
}
