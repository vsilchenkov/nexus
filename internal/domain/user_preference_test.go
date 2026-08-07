package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"nexus/internal/domain"
)

func TestUserPreference_Validate_Key(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr error
	}{
		{"simple", "period", nil},
		{"namespaced", domain.PreferenceKeyOverviewPeriod, nil},
		{"node metrics view", domain.PreferenceKeyNodeMetricsView("e557e4b9-55ce-455c-8bfc-9996d568a7c6"), nil},
		{"deep namespace", "overview.logs.filters_v2", nil},
		{"digits and underscore", "a1_b2.c3_d4", nil},
		{"max length", strings.Repeat("a", 64), nil},

		{"empty", "", domain.ErrPreferenceKeyInvalid},
		{"too long", strings.Repeat("a", 65), domain.ErrPreferenceKeyInvalid},
		{"upper case", "Overview.Period", domain.ErrPreferenceKeyInvalid},
		{"leading digit", "1overview", domain.ErrPreferenceKeyInvalid},
		{"leading dot", ".overview", domain.ErrPreferenceKeyInvalid},
		{"trailing dot", "overview.", domain.ErrPreferenceKeyInvalid},
		{"double dot", "overview..period", domain.ErrPreferenceKeyInvalid},
		{"dash", "overview-period", domain.ErrPreferenceKeyInvalid},
		{"space", "overview period", domain.ErrPreferenceKeyInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &domain.UserPreference{Key: tt.key, Value: json.RawMessage(`{"kind":"preset"}`)}
			err := p.Validate()

			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tt.wantErr), "got %v, want %v", err, tt.wantErr)
		})
	}
}

// §84.3: ключ вида узла собирается из UUID, и оба ограничения §71 — формат и
// длина — он обязан проходить с запасом. Проверяем не только «валиден», но и
// сам запас: удлинение префикса однажды упрётся в 400 при сохранении, и
// узнать об этом надо здесь, а не на бою.
func TestPreferenceKeyNodeMetricsView(t *testing.T) {
	t.Parallel()

	const nodeID = "e557e4b9-55ce-455c-8bfc-9996d568a7c6"
	key := domain.PreferenceKeyNodeMetricsView(nodeID)

	assert.Equal(t, "node.metrics.view.e557e4b955ce455c8bfc9996d568a7c6", key)
	assert.NotContains(t, key, "-", "дефисы не проходят формат ключа §71")
	assert.Len(t, key, 50)
	assert.LessOrEqual(t, len(key), 64, "потолок VARCHAR(64) и maxPreferenceKeyLen")

	p := &domain.UserPreference{Key: key, Value: json.RawMessage(`{"range":"24h","step":"1h"}`)}
	assert.NoError(t, p.Validate())

	// Разные узлы дают разные ключи — иначе вид одного узла затирал бы другой.
	other := domain.PreferenceKeyNodeMetricsView("2c7b4368-d2d0-4795-aa9a-7184e2ea697c")
	assert.NotEqual(t, key, other)
	assert.NoError(t, (&domain.UserPreference{Key: other, Value: json.RawMessage(`{}`)}).Validate())
}

func TestUserPreference_Validate_Value(t *testing.T) {
	t.Parallel()

	// Ровно 4096 байт: {"a":"<4086 x>"} = 8 служебных символов + 4088 внутри.
	maxValue := `{"a":"` + strings.Repeat("x", 4096-8) + `"}`
	assertLen(t, maxValue, 4096)

	tests := []struct {
		name    string
		value   json.RawMessage
		wantErr error
	}{
		{"object", json.RawMessage(`{"kind":"preset","range":"7d"}`), nil},
		{"array", json.RawMessage(`[1,2,3]`), nil},
		{"string", json.RawMessage(`"7d"`), nil},
		{"number", json.RawMessage(`42`), nil},
		{"false is a value", json.RawMessage(`false`), nil},
		{"max size", json.RawMessage(maxValue), nil},

		{"nil", nil, domain.ErrPreferenceValueInvalid},
		{"empty", json.RawMessage(``), domain.ErrPreferenceValueInvalid},
		{"malformed", json.RawMessage(`{oops`), domain.ErrPreferenceValueInvalid},
		{"json null", json.RawMessage(`null`), domain.ErrPreferenceValueInvalid},
		{"json null padded", json.RawMessage("  null\n"), domain.ErrPreferenceValueInvalid},
		{"over size", json.RawMessage(maxValue + " "), domain.ErrPreferenceValueInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &domain.UserPreference{Key: domain.PreferenceKeyOverviewPeriod, Value: tt.value}
			err := p.Validate()

			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tt.wantErr), "got %v, want %v", err, tt.wantErr)
		})
	}
}

func TestUserPreference_IsGlobal(t *testing.T) {
	t.Parallel()

	assert.True(t, (&domain.UserPreference{}).IsGlobal())
	assert.False(t, (&domain.UserPreference{TeamID: "11111111-1111-1111-1111-111111111111"}).IsGlobal())
}

// assertLen — страховка от опечатки в вычислении граничного значения: тест на
// границу размера бессмысленен, если строка на самом деле не той длины.
func assertLen(t *testing.T, s string, want int) {
	t.Helper()
	assert.Len(t, s, want)
}
