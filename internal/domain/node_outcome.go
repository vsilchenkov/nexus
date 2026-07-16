package domain

// NodeOutcome — исход ПОСЛЕДНЕГО исходящего вызова узла (§52). Управляет
// runtime-бейджем узла на Overview (OK/Degraded/Down). Не путать с
// конфигурационным NodeStatus (enabled/paused/disabled).
type NodeOutcome string

const (
	// NodeOutcomeOK — узел ответил 2xx.
	NodeOutcomeOK NodeOutcome = "ok"
	// NodeOutcomeDegraded — узел ответил, но не-2xx и < 500 (1xx/3xx/4xx):
	// ошибка данных/клиента, узел жив (§50.4 — breaker по таким не открывается).
	NodeOutcomeDegraded NodeOutcome = "degraded"
	// NodeOutcomeDown — транспортная ошибка (status 0) или ответ >= 500,
	// включая синтезированные шиной 503 breaker-open и 502 oversize (§43-rev):
	// полезная нагрузка не доставлена.
	NodeOutcomeDown NodeOutcome = "down"
)

func (o NodeOutcome) Valid() bool {
	switch o {
	case NodeOutcomeOK, NodeOutcomeDegraded, NodeOutcomeDown:
		return true
	}
	return false
}

// IsError сохраняет семантику прежнего булевого флага «последний вызов
// ошибочен» (любой не-2xx): им живут nexus_request_incomplete_total и
// back-compat поле API last_error.
func (o NodeOutcome) IsError() bool {
	return o != NodeOutcomeOK
}

// OutcomeFromStatusCode классифицирует исход по HTTP-статусу ответа.
// Граница < 500 — та же, что у upstreamHealthy в §50.4 («узел жив и отвечает»).
// StatusCode <= 0 означает транспортную ошибку (ответа не было).
func OutcomeFromStatusCode(code int32) NodeOutcome {
	switch {
	case code >= 200 && code < 300:
		return NodeOutcomeOK
	case code <= 0 || code >= 500:
		return NodeOutcomeDown
	default:
		return NodeOutcomeDegraded
	}
}

// GaugeValue — кодировка для Prometheus-гауджа nexus_node_last_request_error
// (§41/§52): 0=ok, 1=degraded, 2=down. Порядок важен: `max by (node)` между
// репликами Sender выбирает худшее состояние; старые алерты `>= 1` продолжают
// ловить любую проблему. НЕ совпадает с Redis-кодировкой §46 (там "1"=down,
// "2"=degraded ради legacy-совместимости) — см. platform/nodestatus.
func (o NodeOutcome) GaugeValue() float64 {
	switch o {
	case NodeOutcomeDegraded:
		return 1
	case NodeOutcomeDown:
		return 2
	default:
		return 0
	}
}

// OutcomeFromGaugeValue — обратное к GaugeValue толкование значения гауджа
// (Prometheus-fallback в Web §41). Значение 1 от старого Sender (булев
// «любой не-2xx») транзиентно толкуется как degraded — Redis приоритетен,
// обновится следующим вызовом узла.
func OutcomeFromGaugeValue(v float64) NodeOutcome {
	switch {
	case v >= 2:
		return NodeOutcomeDown
	case v >= 1:
		return NodeOutcomeDegraded
	default:
		return NodeOutcomeOK
	}
}
