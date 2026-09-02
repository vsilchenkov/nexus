package receiver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSyncBodyLimitFitsGRPC — тело предельного размера обязано влезать в одно
// gRPC-сообщение вместе с envelope. Расхождение даёт клиенту 502
// (ResourceExhausted) вместо честного 413, поэтому проверка нужна на старте.
func TestSyncBodyLimitFitsGRPC(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20

	tests := []struct {
		name    string
		maxBody int
		grpcMax int
		want    bool
	}{
		{
			name:    "поставка 1.34.0: 300 МиБ тела в 384 МиБ gRPC",
			maxBody: 300 * mib,
			grpcMax: 384 * mib,
			want:    true,
		},
		{
			name:    "поставка до 1.34.0: 124 МиБ тела в 192 МиБ gRPC",
			maxBody: 124 * mib,
			grpcMax: 192 * mib,
			want:    true,
		},
		{
			name:    "откат кода без отката конфигурации: 300 МиБ тела в 192 МиБ gRPC",
			maxBody: 300 * mib,
			grpcMax: 192 * mib,
			want:    false,
		},
		{
			name:    "подъём тела без подъёма gRPC: дефолт gRPC 64 МиБ",
			maxBody: 300 * mib,
			grpcMax: 64 * mib,
			want:    false,
		},
		{
			name:    "тело ровно по размеру gRPC — envelope уже не влезает",
			maxBody: 192 * mib,
			grpcMax: 192 * mib,
			want:    false,
		},
		{
			name:    "тело на границе: ровно gRPC минус запас под envelope",
			maxBody: 192*mib - grpcRequestEnvelopeReserve,
			grpcMax: 192 * mib,
			want:    true,
		},
		{
			name:    "на байт больше границы",
			maxBody: 192*mib - grpcRequestEnvelopeReserve + 1,
			grpcMax: 192 * mib,
			want:    false,
		},
		{
			name:    "лимит тела не задан — проверять нечего (нормализуют дефолты)",
			maxBody: 0,
			grpcMax: 64 * mib,
			want:    true,
		},
		{
			name:    "лимит gRPC не задан — проверять нечего",
			maxBody: 300 * mib,
			grpcMax: 0,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, syncBodyLimitFitsGRPC(tt.maxBody, tt.grpcMax))
		})
	}
}
