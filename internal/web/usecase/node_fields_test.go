package usecase

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// diffFixtureOld / diffFixtureNew — два узла, различающиеся в КАЖДОМ поле,
// которое пишет diffNodes. Нужны сторожу формата (§108.2): реестр полей строит
// diff аудита, и запись обязана остаться прежней байт-в-байт, иначе старые и
// новые строки журнала разойдутся по форме.
func diffFixtureOld() *domain.Node {
	return &domain.Node{
		Path: "old/path", TargetURL: "http://old:1", URLMode: domain.URLModeStatic,
		AuthType: domain.AuthTypeNone, IncomingAuthType: domain.IncomingAuthTypeNone,
		Status: domain.NodeStatusEnabled, TimeoutMs: 1000, RetryCount: 1,
		WebhookSignatureHeader: "X-Old", WebhookSignaturePrefix: "old=",
		ExternalTable: false, CircuitBreakerThreshold: 5, CircuitBreakerCooldownSec: 30,
		SSEDecline: false, BreakerIgnoreTimeouts: false,
		AuthCredentials: "a", IncomingAuthCredentials: "b",
		RMQHost: "rmq-old", RMQPort: 5672, RMQVHost: "/", RMQUser: "u1", RMQQueue: "q1",
		RMQUseTLS: false, PullIntervalSec: 5, PullBatchSize: 100, PullPrefetch: 100,
		RMQPassword: "p1",
		AsyncAck:    auditSpec(`{"a":1}`),
	}
}

func diffFixtureNew() *domain.Node {
	return &domain.Node{
		Path: "new/path", TargetURL: "http://new:2", URLMode: domain.URLModeFromRequest,
		AuthType: domain.AuthTypeToken, IncomingAuthType: domain.IncomingAuthTypeWebhookSignature,
		Status: domain.NodeStatusPaused, TimeoutMs: 2000, RetryCount: 3,
		WebhookSignatureHeader: "X-New", WebhookSignaturePrefix: "new=",
		ExternalTable: true, CircuitBreakerThreshold: 10, CircuitBreakerCooldownSec: 60,
		SSEDecline: true, BreakerIgnoreTimeouts: true,
		AuthCredentials: "a2", IncomingAuthCredentials: "b2",
		RMQHost: "rmq-new", RMQPort: 5671, RMQVHost: "/v", RMQUser: "u2", RMQQueue: "q2",
		RMQUseTLS: true, PullIntervalSec: 7, PullBatchSize: 50, PullPrefetch: 25,
		RMQPassword: "p2",
		AsyncAck:    auditSpec(`{"a":2}`),
	}
}

func beforeAfter(before, after any) map[string]any {
	return map[string]any{"before": before, "after": after}
}

// TestDiffNodes_FullFormatGolden — сторож формата записи node.update: ключи,
// значения И Go-типы значений (перечисления — string, числа — int32). Типы
// важны не только для JSON: тесты и потребители сравнивают карты напрямую.
func TestDiffNodes_FullFormatGolden(t *testing.T) {
	t.Parallel()

	old, updated := diffFixtureOld(), diffFixtureNew()
	want := map[string]any{
		"path":                         beforeAfter("old/path", "new/path"),
		"target_url":                   beforeAfter("http://old:1", "http://new:2"),
		"url_mode":                     beforeAfter("static", "from_request"),
		"auth_type":                    beforeAfter("none", "token"),
		"incoming_auth_type":           beforeAfter("none", "webhook_signature"),
		"status":                       beforeAfter("enabled", "paused"),
		"timeout_ms":                   beforeAfter(int32(1000), int32(2000)),
		"retry_count":                  beforeAfter(int32(1), int32(3)),
		"webhook_signature_header":     beforeAfter("X-Old", "X-New"),
		"webhook_signature_prefix":     beforeAfter("old=", "new="),
		"external_table":               beforeAfter(false, true),
		"circuit_breaker_threshold":    beforeAfter(int32(5), int32(10)),
		"circuit_breaker_cooldown_sec": beforeAfter(int32(30), int32(60)),
		"sse_decline":                  beforeAfter(false, true),
		"breaker_ignore_timeouts":      beforeAfter(false, true),
		"auth_credentials":             "changed",
		"incoming_auth_credentials":    "changed",
		"rmq_host":                     beforeAfter("rmq-old", "rmq-new"),
		"rmq_port":                     beforeAfter(int32(5672), int32(5671)),
		"rmq_vhost":                    beforeAfter("/", "/v"),
		"rmq_user":                     beforeAfter("u1", "u2"),
		"rmq_queue":                    beforeAfter("q1", "q2"),
		"rmq_use_tls":                  beforeAfter(false, true),
		"pull_interval_sec":            beforeAfter(int32(5), int32(7)),
		"pull_batch_size":              beforeAfter(int32(100), int32(50)),
		"pull_prefetch":                beforeAfter(int32(100), int32(25)),
		"rmq_password":                 "changed",
		"async_ack_spec":               beforeAfter(ackSpecAudit(old.AsyncAck), ackSpecAudit(updated.AsyncAck)),
	}

	got := diffNodes(old, updated)
	assert.Equal(t, want, got)

	// Та же проверка на уровне байтов JSONB: именно это попадает в user_audit.
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantJSON), string(gotJSON))
}

// TestDiffNodes_EqualNodesEmpty — одинаковые узлы не дают ни одного ключа.
func TestDiffNodes_EqualNodesEmpty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, diffNodes(diffFixtureOld(), diffFixtureOld()))
}

// TestNodeFieldRegistry_CoversDiff — ключи diff двух полностью разных узлов =
// поля реестра ∪ секреты ∪ сводка шаблона. Поле, которое попало в аудит мимо
// реестра, не было бы отменяемым; поле реестра вне аудита — отменять нечего.
func TestNodeFieldRegistry_CoversDiff(t *testing.T) {
	t.Parallel()

	want := map[string]bool{auditKeyAckSpec: true}
	for _, f := range nodeFieldRegistry() {
		require.False(t, want[f.name], "дубль поля %q", f.name)
		want[f.name] = true
	}
	for _, s := range nodeSecretRegistry() {
		want[s.name] = true
	}
	got := map[string]bool{}
	for k := range diffNodes(diffFixtureOld(), diffFixtureNew()) {
		got[k] = true
	}
	assert.Equal(t, want, got)
}

// TestNodeField_SetRoundTripFromJSONB — значение, прошедшее JSONB (числа
// становятся float64), восстанавливается в поле узла в исходном типе.
func TestNodeField_SetRoundTripFromJSONB(t *testing.T) {
	t.Parallel()

	src := diffFixtureNew()
	for _, f := range nodeFieldRegistry() {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(f.get(src))
			require.NoError(t, err)
			var fromDB any
			require.NoError(t, json.Unmarshal(raw, &fromDB))

			dst := diffFixtureOld()
			require.NoError(t, f.set(dst, fromDB))
			assert.Equal(t, f.get(src), f.get(dst))

			ok, err := f.holds(dst, fromDB)
			require.NoError(t, err)
			assert.True(t, ok, "восстановленное значение должно совпасть со значением журнала")
		})
	}
}

// TestNodeField_SetRejects — значения, которые нельзя положить в поле без
// потерь, отклоняются, а поле узла не меняется.
func TestNodeField_SetRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		field string
		value any
	}{
		{"null into int32", "timeout_ms", nil},
		{"fraction into int32", "timeout_ms", 1.5},
		{"int32 overflow", "timeout_ms", 1e12},
		{"string into int32", "retry_count", "5"},
		{"string into bool", "external_table", "x"},
		{"bool into string", "target_url", true},
		{"number into enum", "status", 1.0},
		{"object into string", "path", map[string]any{"a": 1.0}},
		{"null into string", "path", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, ok := lookupNodeField(tt.field)
			require.True(t, ok)
			n := diffFixtureOld()
			before := f.get(n)
			require.Error(t, f.set(n, tt.value))
			assert.Equal(t, before, f.get(n), "поле не должно меняться при ошибке")
			_, err := f.holds(n, tt.value)
			assert.Error(t, err)
		})
	}
}

// TestNodeField_SetStatusDetailsShape — SetStatus пишет map[string]string, из
// памяти приходят int32 и перечисления: всё разбирается одинаково.
func TestNodeField_SetStatusDetailsShape(t *testing.T) {
	t.Parallel()

	status, _ := lookupNodeField("status")
	n := &domain.Node{Status: domain.NodeStatusEnabled}
	require.NoError(t, status.set(n, "paused"))
	assert.Equal(t, domain.NodeStatusPaused, n.Status)

	timeout, _ := lookupNodeField("timeout_ms")
	ok, err := timeout.holds(&domain.Node{TimeoutMs: 30000}, int32(30000))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = timeout.holds(&domain.Node{TimeoutMs: 30000}, float64(30000))
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = timeout.holds(&domain.Node{TimeoutMs: 30000}, float64(1000))
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestNodeField_Applies — rmq_*/pull_* относятся только к pull-узлу: у узла
// другого типа репозиторий пишет их NULL (§108.3, inapplicable).
func TestNodeField_Applies(t *testing.T) {
	t.Parallel()

	request := &domain.Node{RootMethod: domain.RootMethodRequest}
	pull := &domain.Node{RootMethod: domain.RootMethodRabbitMQAsync}
	pullOnly := map[string]bool{
		"rmq_host": true, "rmq_port": true, "rmq_vhost": true, "rmq_user": true,
		"rmq_queue": true, "rmq_use_tls": true, "pull_interval_sec": true,
		"pull_batch_size": true, "pull_prefetch": true,
	}
	for _, f := range nodeFieldRegistry() {
		assert.True(t, f.appliesTo(pull), f.name)
		assert.Equal(t, !pullOnly[f.name], f.appliesTo(request), f.name)
	}
}

// TestLookupNodeField_Unknown — секреты и сводка шаблона в реестре значений
// отсутствуют: их нельзя вернуть из журнала.
func TestLookupNodeField_Unknown(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"auth_credentials", "rmq_password", auditKeyAckSpec, "comment", ""} {
		_, ok := lookupNodeField(name)
		assert.False(t, ok, name)
	}
}
