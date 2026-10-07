package metrics

import "nexus/internal/domain"

// NodeLabels — метки узла в per-node метриках (§101.3).
//
// Путь уникален лишь внутри команды, поэтому узел в рядах Prometheus — пара
// «слог команды + путь». Пустые значения значимы: {"", ""} — выход до резолва
// или маршрут не про узел, {"", NodeUnresolved} — запрос к несуществующему узлу.
// Значения берутся только из резолвнутого узла, никогда из адреса запроса.
//
// Per-node векторы пишутся только методами ниже. Позиционный WithLabelValues с
// неверным числом значений паникует в рантайме, а перепутанный порядок молча
// пишет путь в метку команды; guard-тест пакета запрещает такие вызовы вне него.
type NodeLabels struct {
	Team string
	Node string
}

// IncRequest — один запрос к узлу с итоговым статусом (nexus_requests_total).
// status — HTTP-код строкой либо маркер исхода без вызова (paused, cancelled, …).
func (m *Metrics) IncRequest(method string, n NodeLabels, status string) {
	m.RequestsTotal.WithLabelValues(method, n.Team, n.Node, status).Inc()
}

// IncPausedRequeued — сообщение узла на паузе снова переложено в хвост очереди
// паузы (§113.5). Первое откладывание — IncRequest(..., "paused").
func (m *Metrics) IncPausedRequeued(n NodeLabels) {
	m.PausedRequeuedTotal.WithLabelValues(n.Team, n.Node).Inc()
}

// ObserveRequestDuration — длительность запроса к узлу в секундах.
func (m *Metrics) ObserveRequestDuration(method string, n NodeLabels, seconds float64) {
	m.RequestDuration.WithLabelValues(method, n.Team, n.Node).Observe(seconds)
}

// IncRequestIncomplete — исходящий вызов Sender'а не завершился 2xx (§22).
func (m *Metrics) IncRequestIncomplete(method string, n NodeLabels) {
	m.RequestsIncompleteTotal.WithLabelValues(method, n.Team, n.Node).Inc()
}

// SetNodeLastRequestOutcome фиксирует исход последнего исходящего вызова узла
// (§41/§52): 0=ok (2xx), 1=degraded (ответил не-2xx <500), 2=down (транспортная
// ошибка или 5xx). Порядок значений важен: PromQL `max by (team, node)` между
// репликами Sender выбирает худшее состояние. Управляет бейджем узла в Overview.
func (m *Metrics) SetNodeLastRequestOutcome(n NodeLabels, outcome domain.NodeOutcome) {
	m.NodeLastRequestError.WithLabelValues(n.Team, n.Node).Set(outcome.GaugeValue())
}

// IncAsyncIngressRejected — §82.3: обращение во внешний /requestAsync к узлу,
// который не настроен асинхронным.
func (m *Metrics) IncAsyncIngressRejected(n NodeLabels) {
	m.AsyncIngressRejectedTotal.WithLabelValues(n.Team, n.Node).Inc()
}

// IncAckRenderFailed — §83: шаблон ответа приёма не отработал. reason берётся
// из замкнутого множества ackspec.Reason (плюс "compile"/"unknown"), свободные
// строки сюда попадать не должны — иначе кардинальность метрики не ограничена.
func (m *Metrics) IncAckRenderFailed(n NodeLabels, reason string) {
	m.AckRenderFailedTotal.WithLabelValues(n.Team, n.Node, reason).Inc()
}

// IncRMQPulled — §27: сообщение pull-узла обработано; status — ok | nack | reject.
func (m *Metrics) IncRMQPulled(n NodeLabels, status string) {
	m.RMQMessagesPulledTotal.WithLabelValues(n.Team, n.Node, status).Inc()
}

// ObserveRMQPull — §27: длительность выборки и публикации батча pull-узла.
func (m *Metrics) ObserveRMQPull(n NodeLabels, seconds float64) {
	m.RMQPullDuration.WithLabelValues(n.Team, n.Node).Observe(seconds)
}

// SetRMQConnectionState — §27: 0=down, 1=connecting, 2=up.
func (m *Metrics) SetRMQConnectionState(n NodeLabels, state float64) {
	m.RMQConnectionState.WithLabelValues(n.Team, n.Node).Set(state)
}

// SetRMQQueueStats — §27: глубина исходной очереди и число её потребителей.
func (m *Metrics) SetRMQQueueStats(n NodeLabels, depth, consumers float64) {
	m.RMQQueueDepth.WithLabelValues(n.Team, n.Node).Set(depth)
	m.RMQConsumerCount.WithLabelValues(n.Team, n.Node).Set(consumers)
}

// SetNodeDegraded — §52: признак деградации pull-узла по причине reason.
func (m *Metrics) SetNodeDegraded(n NodeLabels, reason string, degraded bool) {
	v := 0.0
	if degraded {
		v = 1
	}
	m.NodeDegraded.WithLabelValues(n.Team, n.Node, reason).Set(v)
}

// IncDLQReprocess — §36: исход обработки одного DLQ-сообщения репроцессором.
func (m *Metrics) IncDLQReprocess(n NodeLabels, result string) {
	m.DLQReprocessTotal.WithLabelValues(n.Team, n.Node, result).Inc()
}

// IncReplayBodySource реализует usecase.ReplayMetrics (§96.9): откуда взято
// тело повтора. source — queue | log | rejected.
func (m *Metrics) IncReplayBodySource(n NodeLabels, source string) {
	m.ReplayBodySourceTotal.WithLabelValues(n.Team, n.Node, source).Inc()
}
