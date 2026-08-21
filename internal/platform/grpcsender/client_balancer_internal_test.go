package grpcsender

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolverTarget (§93.4): адрес из конфига превращается в dns-target, но
// явно заданная схема сохраняется как есть.
//
// Смысл проверки — не форматирование строки, а то, что балансировка вообще
// включится: с target'ом без резолвера gRPC видит один адрес, round_robin ему
// не по чему раскладывать, и вторая реплика Sender'а простаивает.
func TestResolverTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want string
	}{
		{"host:port получает dns-резолвер", "sender:9190", "dns:///sender:9190"},
		{"ip:port получает dns-резолвер", "10.0.0.5:9190", "dns:///10.0.0.5:9190"},
		{"явный dns-target не трогаем", "dns:///sender:9190", "dns:///sender:9190"},
		{"unix-сокет не трогаем", "unix:///var/run/sender.sock", "unix:///var/run/sender.sock"},
		{"passthrough оператора не трогаем", "passthrough:///sender:9190", "passthrough:///sender:9190"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, resolverTarget(tt.addr))
		})
	}
}

// TestBalancerServiceConfigIsValidJSON (§93.4): service config уходит в gRPC
// строкой и разбирается уже внутри библиотеки.
//
// Опечатка в нём НЕ роняет клиент: grpc.NewClient с невалидным
// WithDefaultServiceConfig возвращает ошибку только при разборе, а неизвестное
// поле молча игнорируется — балансировка тихо осталась бы pick_first, и
// обнаружилось бы это перекосом нагрузки на бою. Поэтому структуру проверяем
// здесь: обязаны присутствовать round_robin и healthCheckConfig.
func TestBalancerServiceConfigIsValidJSON(t *testing.T) {
	t.Parallel()

	var parsed struct {
		LoadBalancingConfig []map[string]any `json:"loadBalancingConfig"`
		HealthCheckConfig   *struct {
			ServiceName *string `json:"serviceName"`
		} `json:"healthCheckConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(balancerServiceConfig), &parsed),
		"balancerServiceConfig должен быть валидным JSON")

	require.Len(t, parsed.LoadBalancingConfig, 1)
	_, hasRoundRobin := parsed.LoadBalancingConfig[0]["round_robin"]
	assert.True(t, hasRoundRobin,
		"без round_robin пул соединений целиком уляжется в одну реплику Sender'а")

	require.NotNil(t, parsed.HealthCheckConfig, "без healthCheckConfig мёртвая реплика останется в ротации")
	require.NotNil(t, parsed.HealthCheckConfig.ServiceName)
	assert.Empty(t, *parsed.HealthCheckConfig.ServiceName,
		"пустое имя = здоровье сервера целиком, ровно как его выставляет Sender")
}
