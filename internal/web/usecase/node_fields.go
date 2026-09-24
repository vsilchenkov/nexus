package usecase

import (
	"encoding/json"
	"errors"
	"fmt"

	"nexus/internal/domain"
)

// auditSecretChanged — маркер изменения секрета в diff аудита: значение
// пароля или токена в журнал не пишется никогда (§7.13), только факт.
const auditSecretChanged = "changed"

// auditKeyAckSpec — ключ сводки шаблона ответа приёма (§83) в diff аудита.
const auditKeyAckSpec = "async_ack_spec"

// errAuditValueNull — в журнале у поля null: во что его восстанавливать,
// неизвестно (null в int32 json молча пропустил бы, оставив прежнее значение).
var errAuditValueNull = errors.New("audit value is null")

// nodeField — поле узла, которое аудит пишет со значениями «до/после» и
// которое поэтому можно вернуть из журнала (§108.2). Реестр — единственный
// список таких полей: по нему строится и diff node.update, и отмена, так что
// поле, добавленное в аудит, сразу становится отменяемым.
type nodeField struct {
	name string
	// get — значение для записи аудита: перечисления как string, числа как
	// int32. Типы важны — формат строк журнала обязан остаться прежним.
	get func(n *domain.Node) any
	// set — записывает значение из журнала (после JSONB это float64/bool/
	// string) в типизированное поле. null, дробь, переполнение int32 и чужой
	// тип — ошибка.
	set func(n *domain.Node, v any) error
	// applies — относится ли поле к узлу такого типа; nil — к любому.
	applies func(n *domain.Node) bool
}

// appliesTo сообщает, осмысленно ли поле для узла n.
func (f nodeField) appliesTo(n *domain.Node) bool {
	return f.applies == nil || f.applies(n)
}

// holds сообщает, равно ли текущее значение поля узла n значению v из
// журнала. Сравнение идёт в типе поля: float64 30000 из JSONB и int32 30000
// в узле — одно и то же значение.
func (f nodeField) holds(n *domain.Node, v any) (bool, error) {
	probe := *n // set трогает только скалярное поле — поверхностной копии хватает
	if err := f.set(&probe, v); err != nil {
		return false, err
	}
	return f.get(&probe) == f.get(n), nil
}

// nodeFieldRegistry — поля узла со значениями в аудите, в порядке показа.
// Функция, а не пакетная переменная: правило репозитория — без глобального
// состояния; два десятка замыканий на вызов ничего не стоят.
func nodeFieldRegistry() []nodeField {
	pull := func(n *domain.Node) bool { return n.RootMethod.IsPull() }
	return []nodeField{
		plainField("path", func(n *domain.Node) *string { return &n.Path }),
		plainField("target_url", func(n *domain.Node) *string { return &n.TargetURL }),
		enumField("url_mode", func(n *domain.Node) *domain.URLMode { return &n.URLMode }),
		enumField("auth_type", func(n *domain.Node) *domain.AuthType { return &n.AuthType }),
		enumField("incoming_auth_type", func(n *domain.Node) *domain.IncomingAuthType { return &n.IncomingAuthType }),
		enumField("status", func(n *domain.Node) *domain.NodeStatus { return &n.Status }),
		plainField("timeout_ms", func(n *domain.Node) *int32 { return &n.TimeoutMs }),
		plainField("retry_count", func(n *domain.Node) *int32 { return &n.RetryCount }),
		plainField("webhook_signature_header", func(n *domain.Node) *string { return &n.WebhookSignatureHeader }),
		plainField("webhook_signature_prefix", func(n *domain.Node) *string { return &n.WebhookSignaturePrefix }),
		// §64: переключение внешней таблицы меняет, управляет ли Nexus схемой и
		// retention этой таблицы, — такое решение должно быть видно в аудите.
		plainField("external_table", func(n *domain.Node) *bool { return &n.ExternalTable }),
		// §81.3: ослабление защиты узла (порог выше, пауза короче) должно быть
		// видно в аудите — иначе «почему он опять долбит мёртвый приёмник» не разобрать.
		plainField("circuit_breaker_threshold", func(n *domain.Node) *int32 { return &n.CircuitBreakerThreshold }),
		plainField("circuit_breaker_cooldown_sec", func(n *domain.Node) *int32 { return &n.CircuitBreakerCooldownSec }),
		// §107: оба флага меняют, что видят клиент узла (405 вместо подписки) и
		// защита (таймаут перестаёт открывать breaker), — решение для аудита.
		plainField("sse_decline", func(n *domain.Node) *bool { return &n.SSEDecline }),
		plainField("breaker_ignore_timeouts", func(n *domain.Node) *bool { return &n.BreakerIgnoreTimeouts }),
		// §27: параметры RabbitMQAsync. У узла другого типа репозиторий пишет их
		// NULL — значение из журнала туда не сохранится (§108.3, inapplicable).
		withApplies(plainField("rmq_host", func(n *domain.Node) *string { return &n.RMQHost }), pull),
		withApplies(plainField("rmq_port", func(n *domain.Node) *int32 { return &n.RMQPort }), pull),
		withApplies(plainField("rmq_vhost", func(n *domain.Node) *string { return &n.RMQVHost }), pull),
		withApplies(plainField("rmq_user", func(n *domain.Node) *string { return &n.RMQUser }), pull),
		withApplies(plainField("rmq_queue", func(n *domain.Node) *string { return &n.RMQQueue }), pull),
		withApplies(plainField("rmq_use_tls", func(n *domain.Node) *bool { return &n.RMQUseTLS }), pull),
		withApplies(plainField("pull_interval_sec", func(n *domain.Node) *int32 { return &n.PullIntervalSec }), pull),
		withApplies(plainField("pull_batch_size", func(n *domain.Node) *int32 { return &n.PullBatchSize }), pull),
		withApplies(plainField("pull_prefetch", func(n *domain.Node) *int32 { return &n.PullPrefetch }), pull),
	}
}

// lookupNodeField ищет поле реестра по имени ключа аудита.
func lookupNodeField(name string) (nodeField, bool) {
	for _, f := range nodeFieldRegistry() {
		if f.name == name {
			return f, true
		}
	}
	return nodeField{}, false
}

// nodeSecret — секрет узла: в аудит пишется только факт изменения.
type nodeSecret struct {
	name string
	get  func(n *domain.Node) string
	// coupled — поля, чей смысл держится на этом секрете: вернуть их без
	// секрета значит собрать узел из двух состояний (§108.3, credentials).
	coupled []string
}

// nodeSecretRegistry — секреты узла, изменение которых видно в аудите.
func nodeSecretRegistry() []nodeSecret {
	return []nodeSecret{
		{
			name:    "auth_credentials",
			get:     func(n *domain.Node) string { return n.AuthCredentials },
			coupled: []string{"auth_type"},
		},
		{
			name:    "incoming_auth_credentials",
			get:     func(n *domain.Node) string { return n.IncomingAuthCredentials },
			coupled: []string{"incoming_auth_type", "webhook_signature_header", "webhook_signature_prefix"},
		},
		{
			// §27: rmq_password маскируется, как остальные креды.
			name:    "rmq_password",
			get:     func(n *domain.Node) string { return n.RMQPassword },
			coupled: []string{"rmq_host", "rmq_port", "rmq_vhost", "rmq_user", "rmq_queue", "rmq_use_tls"},
		},
	}
}

// diffSecretsAndAck дописывает в diff секреты (только факт изменения) и
// сводку шаблона ответа приёма. §83: шаблон меняет то, что видит КЛИЕНТ узла,
// — включение, выключение и правка обязаны быть в аудите. Секретом он не
// является, поэтому пишется значение, а не «changed»; шаблон при этом
// обрезается — в журнале нужен факт и узнаваемый фрагмент, а не 8 КиБ текста.
func diffSecretsAndAck(d map[string]any, old, n *domain.Node) {
	for _, s := range nodeSecretRegistry() {
		if s.get(old) != s.get(n) {
			d[s.name] = auditSecretChanged
		}
	}
	if before, after := ackSpecAudit(old.AsyncAck), ackSpecAudit(n.AsyncAck); before != after {
		d[auditKeyAckSpec] = map[string]any{"before": before, "after": after}
	}
}

// plainField — поле, которое пишется в аудит в собственном типе.
func plainField[T comparable](name string, ptr func(*domain.Node) *T) nodeField {
	return scalarField(name, ptr, func(v T) any { return v })
}

// enumField — строковое перечисление: в аудит пишется как plain string.
func enumField[T ~string](name string, ptr func(*domain.Node) *T) nodeField {
	return scalarField(name, ptr, func(v T) any { return string(v) })
}

func scalarField[T any](name string, ptr func(*domain.Node) *T, view func(T) any) nodeField {
	return nodeField{
		name: name,
		get:  func(n *domain.Node) any { return view(*ptr(n)) },
		set: func(n *domain.Node, v any) error {
			val, err := decodeAuditValue[T](v)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			*ptr(n) = val
			return nil
		},
	}
}

func withApplies(f nodeField, applies func(*domain.Node) bool) nodeField {
	f.applies = applies
	return f
}

// decodeAuditValue переводит значение из details журнала в тип поля через
// JSON: так float64 из JSONB, int32 из памяти и map[string]string от
// SetStatus ведут себя одинаково, а дробь или переполнение int32 дают
// ошибку, а не молча усечённое число.
func decodeAuditValue[T any](v any) (T, error) {
	var out T
	if v == nil {
		return out, errAuditValueNull
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("encode audit value: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode audit value: %w", err)
	}
	return out, nil
}
