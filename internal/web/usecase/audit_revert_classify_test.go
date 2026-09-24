package usecase

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// fieldByName — поле классификации по имени (тесты).
func fieldByName(t *testing.T, fields []RevertField, name string) RevertField {
	t.Helper()
	for _, f := range fields {
		if f.Name == name {
			return f
		}
	}
	require.Failf(t, "field not classified", "%s", name)
	return RevertField{}
}

// TestClassifyEntry_FieldStatus — статус поля строки журнала относительно
// текущего узла (§108.3). Значения details — в том виде, в каком их отдаёт
// JSONB: числа float64.
func TestClassifyEntry_FieldStatus(t *testing.T) {
	t.Parallel()

	request := func(mut func(n *domain.Node)) *domain.Node {
		n := &domain.Node{RootMethod: domain.RootMethodRequest, TargetURL: "http://b", TimeoutMs: 2000,
			Status: domain.NodeStatusEnabled, AsyncAck: auditSpec(`{"a":1}`)}
		if mut != nil {
			mut(n)
		}
		return n
	}
	tests := []struct {
		name        string
		key         string
		raw         any
		cur         *domain.Node
		wantStatus  RevertFieldStatus
		wantCurrent any
		wantDiffers bool
	}{
		{"current == after", "target_url", beforeAfter("http://a", "http://b"), request(nil), RevertFieldActual, "http://b", false},
		{"overwritten later", "target_url", beforeAfter("http://a", "http://c"), request(nil), RevertFieldOverwritten, "http://b", true},
		{"already reverted", "target_url", beforeAfter("http://b", "http://c"), request(nil), RevertFieldAlready, "http://b", true},
		{"float64 vs int32", "timeout_ms", beforeAfter(1000.0, 2000.0), request(nil), RevertFieldActual, int32(2000), false},
		{"SetStatus map[string]string", "status", map[string]string{"before": "paused", "after": "enabled"}, request(nil), RevertFieldActual, "enabled", false},
		{"secret", "auth_credentials", "changed", request(nil), RevertFieldSecret, nil, false},
		{"pull field on request node", "rmq_host", beforeAfter("h1", "h2"), request(nil), RevertFieldInapplicable, nil, false},
		{"pull field on pull node", "rmq_host", beforeAfter("h1", "h2"),
			request(func(n *domain.Node) { n.RootMethod = domain.RootMethodRabbitMQAsync; n.RMQHost = "h2" }),
			RevertFieldActual, "h2", false},
		{"fraction in int32", "timeout_ms", beforeAfter(1.5, 2000.0), request(nil), RevertFieldUnrecoverable, nil, false},
		{"null before", "path", beforeAfter(nil, "x"), request(nil), RevertFieldUnrecoverable, nil, false},
		{"not a diff shape", "path", "x", request(nil), RevertFieldUnrecoverable, nil, false},
		{"unknown key", "comment", beforeAfter("a", "b"), request(nil), RevertFieldUnrecoverable, nil, false},
		{"node deleted", "target_url", beforeAfter("http://a", "http://b"), nil, RevertFieldUnrecoverable, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := fieldByName(t, classifyEntry(map[string]any{tt.key: tt.raw}, tt.cur), tt.key)
			assert.Equal(t, tt.wantStatus, f.Status)
			assert.Equal(t, tt.wantCurrent, f.Current)
			assert.Equal(t, tt.wantDiffers, f.differs)
		})
	}
}

// TestClassifyEntry_AckSpec — сводка шаблона ответа не отменяется, но
// неактуальность строки по ней видна.
func TestClassifyEntry_AckSpec(t *testing.T) {
	t.Parallel()

	cur := &domain.Node{AsyncAck: auditSpec(`{"a":1}`)}
	now := ackSpecAudit(cur.AsyncAck)

	same := fieldByName(t, classifyEntry(map[string]any{auditKeyAckSpec: beforeAfter("", now)}, cur), auditKeyAckSpec)
	assert.Equal(t, RevertFieldUnrecoverable, same.Status)
	assert.False(t, same.Revertible())
	assert.True(t, same.comparable)
	assert.False(t, same.differs)

	other := fieldByName(t, classifyEntry(map[string]any{auditKeyAckSpec: beforeAfter("", "status=200 …")}, cur), auditKeyAckSpec)
	assert.True(t, other.differs)
}

// TestClassifyEntry_CredentialsWarning — поле, связанное с кредами,
// изменёнными в той же строке, получает предупреждение (§108.3).
func TestClassifyEntry_CredentialsWarning(t *testing.T) {
	t.Parallel()

	cur := &domain.Node{RootMethod: domain.RootMethodRequest, AuthType: domain.AuthTypeToken,
		IncomingAuthType: domain.IncomingAuthTypeNone, TargetURL: "http://b"}
	details := map[string]any{
		"auth_type":          beforeAfter("basic", "token"),
		"auth_credentials":   "changed",
		"incoming_auth_type": beforeAfter("basic", "none"),
		"target_url":         beforeAfter("http://a", "http://b"),
	}
	fields := classifyEntry(details, cur)
	assert.Equal(t, RevertWarningCredentials, fieldByName(t, fields, "auth_type").Warning)
	assert.Empty(t, fieldByName(t, fields, "incoming_auth_type").Warning, "incoming-креды в строке не менялись")
	assert.Empty(t, fieldByName(t, fields, "target_url").Warning)
	assert.Equal(t, map[string]any{"target_url": "http://a", "incoming_auth_type": "basic"}, defaultRevertValues(fields),
		"поле с предупреждением не выбирается по умолчанию")
}

// TestClassifyEntry_IgnoresMetaAndOrders — служебные ключи не поля; порядок —
// порядок реестра, затем секреты, сводка шаблона и неизвестные ключи.
func TestClassifyEntry_IgnoresMetaAndOrders(t *testing.T) {
	t.Parallel()

	details := map[string]any{
		"zzz_unknown":                 beforeAfter(1.0, 2.0),
		auditKeyAckSpec:               beforeAfter("", "x"),
		"rmq_password":                "changed",
		"status":                      beforeAfter("paused", "enabled"),
		"path":                        beforeAfter("a", "b"),
		"reverted_from":               "id",
		"reverted_from_at":            "ts",
		"cleared_incompatible_fields": []any{"sse_decline"},
	}
	fields := classifyEntry(details, &domain.Node{})
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	assert.Equal(t, []string{"path", "status", "rmq_password", auditKeyAckSpec, "zzz_unknown"}, names)
}

// TestRevertState — состояние строки по её полям (§108.3).
func TestRevertState(t *testing.T) {
	t.Parallel()

	cur := &domain.Node{RootMethod: domain.RootMethodRequest, TargetURL: "http://b", Status: domain.NodeStatusEnabled,
		AsyncAck: auditSpec(`{"a":1}`)}
	tests := []struct {
		name           string
		details        map[string]any
		cur            *domain.Node
		wantState      RevertState
		wantRevertible bool
		wantOverwrite  map[string]any
	}{
		{"node gone", map[string]any{"target_url": beforeAfter("http://a", "http://b")}, nil, RevertStateGone, false, nil},
		{"only secrets", map[string]any{"auth_credentials": "changed"}, cur, RevertStateNone, false, nil},
		{"only inapplicable", map[string]any{"rmq_host": beforeAfter("a", "b")}, cur, RevertStateNone, false, nil},
		{"empty details", map[string]any{}, cur, RevertStateNone, false, nil},
		{"all actual", map[string]any{
			"target_url": beforeAfter("http://a", "http://b"),
			"status":     beforeAfter("paused", "enabled"),
		}, cur, RevertStateActual, true, nil},
		{"one overwritten", map[string]any{
			"target_url": beforeAfter("http://a", "http://c"),
			"status":     beforeAfter("paused", "enabled"),
		}, cur, RevertStateStale, true, map[string]any{"target_url": "http://b"}},
		{"fully reverted", map[string]any{"status": beforeAfter("enabled", "paused")}, cur, RevertStateStale, false,
			map[string]any{"status": "enabled"}},
		{"ack spec changed later", map[string]any{auditKeyAckSpec: beforeAfter("", "other")}, cur, RevertStateStale, false,
			map[string]any{auditKeyAckSpec: ackSpecAudit(cur.AsyncAck)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := markOf(classifyEntry(tt.details, tt.cur), tt.cur)
			assert.Equal(t, tt.wantState, m.State)
			assert.Equal(t, tt.wantRevertible, m.Revertible)
			assert.Equal(t, tt.wantOverwrite, m.Overwritten)
		})
	}
}

// TestSelectRevertValues — выбор полей для применения: только отменяемые,
// без дублей, непустой (§108.7).
func TestSelectRevertValues(t *testing.T) {
	t.Parallel()

	cur := &domain.Node{RootMethod: domain.RootMethodRequest, TargetURL: "http://c", TimeoutMs: 2000,
		Status: domain.NodeStatusEnabled}
	fields := classifyEntry(map[string]any{
		"target_url":       beforeAfter("http://a", "http://b"), // overwritten
		"timeout_ms":       beforeAfter(1000.0, 2000.0),         // actual
		"status":           beforeAfter("enabled", "paused"),    // already
		"auth_credentials": "changed",                           // secret
		"rmq_host":         beforeAfter("a", "b"),               // inapplicable
	}, cur)

	got, err := selectRevertValues(fields, []string{"target_url", "timeout_ms"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"target_url": "http://a", "timeout_ms": 1000.0}, got)

	for _, bad := range [][]string{nil, {}, {"status"}, {"auth_credentials"}, {"rmq_host"}, {"comment"}, {"timeout_ms", "timeout_ms"}} {
		_, err := selectRevertValues(fields, bad)
		assert.ErrorIs(t, err, domain.ErrAuditRevertFieldInvalid, "%v", bad)
	}
}
