package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// §98.5: границы глобальной политики защиты узла — зеркало границ поля узла.
// Расхождение дало бы значение, которое принимают глобально и отвергают на
// узле (или наоборот), и оператор не понял бы, какое из двух правил настоящее.
func TestValidateBreakerPolicy_Bounds(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name      string
		threshold int
		cooldown  int
		wantErrT  error
		wantErrC  error
	}{
		{name: "нижняя граница", threshold: 1, cooldown: 1},
		{name: "верхняя граница", threshold: 100, cooldown: 3600},
		{name: "типовые значения", threshold: 5, cooldown: 30},
		{name: "ноль не «как выше», а ошибка", threshold: 0, cooldown: 0,
			wantErrT: domain.ErrBreakerThresholdInvalid, wantErrC: domain.ErrBreakerCooldownInvalid},
		{name: "отрицательные", threshold: -1, cooldown: -1,
			wantErrT: domain.ErrBreakerThresholdInvalid, wantErrC: domain.ErrBreakerCooldownInvalid},
		{name: "выше верхней", threshold: 101, cooldown: 3601,
			wantErrT: domain.ErrBreakerThresholdInvalid, wantErrC: domain.ErrBreakerCooldownInvalid},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, domain.ValidateBreakerThreshold(r.threshold), r.wantErrT)
			assert.ErrorIs(t, domain.ValidateBreakerCooldownSec(r.cooldown), r.wantErrC)
		})
	}
}

// Границы обязаны совпадать с теми, что проверяет Node.Validate.
func TestBreakerBounds_MirrorNodeBounds(t *testing.T) {
	t.Parallel()
	// Максимумы поля узла: порог 100, пауза 3600 (см. Node.Validate).
	n := &domain.Node{
		Path: "test/path", RootMethod: domain.RootMethodRequest,
		TargetURL:                 "https://example.com",
		CircuitBreakerThreshold:   int32(domain.BreakerThresholdMax),
		CircuitBreakerCooldownSec: int32(domain.BreakerCooldownMaxSec),
	}
	n.SetDefaults()
	require.NoError(t, n.Validate(), "верхняя граница глобальной политики обязана приниматься и на узле")

	n.CircuitBreakerThreshold = int32(domain.BreakerThresholdMax) + 1
	assert.Error(t, n.Validate())
}

// §98.5: nil означает «в интерфейсе не задано» — действует конфигурация.
func TestBreakerPolicyOrConfig(t *testing.T) {
	t.Parallel()
	five, sixty := 5, 60

	t.Run("ничего не задано — целиком конфиг", func(t *testing.T) {
		t.Parallel()
		p := domain.BreakerPolicyOrConfig(nil, nil, 7, 90)
		assert.Equal(t, 7, p.Threshold)
		assert.Equal(t, 90*time.Second, p.Cooldown)
	})

	t.Run("задано только одно поле — второе остаётся из конфига", func(t *testing.T) {
		t.Parallel()
		p := domain.BreakerPolicyOrConfig(&five, nil, 7, 90)
		assert.Equal(t, 5, p.Threshold)
		assert.Equal(t, 90*time.Second, p.Cooldown, "незаданное поле не должно обнуляться")
	})

	t.Run("задано всё — конфиг не участвует", func(t *testing.T) {
		t.Parallel()
		p := domain.BreakerPolicyOrConfig(&five, &sixty, 7, 90)
		assert.Equal(t, 5, p.Threshold)
		assert.Equal(t, 60*time.Second, p.Cooldown)
	})

	t.Run("конфиг молчит и настройки молчат — нули, их развернёт breaker", func(t *testing.T) {
		t.Parallel()
		p := domain.BreakerPolicyOrConfig(nil, nil, 0, 0)
		assert.Zero(t, p.Threshold)
		assert.Zero(t, p.Cooldown)
	})
}
