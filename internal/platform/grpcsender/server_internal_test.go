package grpcsender

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"nexus/internal/platform/config"
)

// TestEnforcementPolicy_MappingFromConfig фиксирует перевод конфига в политику
// grpc-go (§82.1).
//
// Белоящичный намеренно: `grpc.ServerOption` непрозрачна, применённую политику
// из неё не прочитать, а проверять нужно именно перевод.
//
// Тонкое место — ИНВЕРСИЯ флага: в конфиге он `deny_ping_without_stream`
// (нужное значение = zero value, иначе дефолт `true` не выразить в yaml), а в
// grpc-go — `PermitWithoutStream`. Перепутанная инверсия отказывает тише всего:
// сервис поднимется, а простаивающее соединение начнёт копить нарушения
// «в кредит» (порог для беспоточных ping'ов — 2 часа) и оборвёт первый же
// долгий вызов.
//
// Длительность вызова здесь не проверяется — это делает интеграционный
// TestSender_GRPCKeepalive_LongUnaryCallSurvives.
func TestEnforcementPolicy_MappingFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		cfg             config.SenderGRPCKeepaliveConfig
		wantMinTime     time.Duration
		wantPermitNoStr bool
	}{
		{
			name:            "defaults_permit_ping_without_stream",
			cfg:             config.SenderGRPCKeepaliveConfig{EnforcementMinTimeSec: 5},
			wantMinTime:     5 * time.Second,
			wantPermitNoStr: true,
		},
		{
			name: "deny_flag_inverts_permit",
			cfg: config.SenderGRPCKeepaliveConfig{
				EnforcementMinTimeSec: 30,
				DenyPingWithoutStream: true,
			},
			wantMinTime:     30 * time.Second,
			wantPermitNoStr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := enforcementPolicy(&tc.cfg)
			assert.Equal(t, tc.wantMinTime, got.MinTime, "MinTime из enforcement_min_time_sec")
			assert.Equal(t, tc.wantPermitNoStr, got.PermitWithoutStream,
				"PermitWithoutStream обязан быть ОТРИЦАНИЕМ deny_ping_without_stream")
		})
	}
}
