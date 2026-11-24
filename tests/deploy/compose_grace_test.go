package deploy

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minStopGrace — нижняя граница stop_grace_period сервиса приложения:
// shutdown.drain_sec + shutdown.timeout_sec из config.yml (5 + 30 с).
//
// Остановка Sender'а доигрывает доставки в полёте (§118.8), Receiver и Web —
// принятые запросы (§93.5), и всё это под потолком timeout_sec после паузы
// drain_sec. Дефолт Docker — 10 с: SIGKILL обрывал бы дренаж (доставка рвётся
// посреди HTTP — повтор после старта) и сброс логов в ClickHouse.
const minStopGrace = 35 * time.Second

// appDockerfiles — признак сервиса приложения: сборка из одного из этих
// Dockerfile'ов (имя сервиса в HA-профиле — sender-1, web-2 и т. п.).
var appDockerfiles = []string{"receiver.Dockerfile", "sender.Dockerfile", "web.Dockerfile"}

func isAppService(svc composeService) bool {
	build, ok := svc["build"].(map[string]any)
	if !ok {
		return false
	}
	df, _ := build["dockerfile"].(string)
	for _, name := range appDockerfiles {
		if strings.HasSuffix(df, name) {
			return true
		}
	}
	return false
}

// TestCompose_AppServicesStopGrace (§118.8): у каждого сервиса приложения во
// всех compose-файлах задан stop_grace_period не меньше drain_sec +
// timeout_sec. До §118 ключ был только в HA-файле (через x-app-base), а в
// одиночных стеках действовал дефолт Docker 10 с.
func TestCompose_AppServicesStopGrace(t *testing.T) {
	t.Parallel()

	checked := 0
	for _, path := range composePaths(t) {
		f := loadCompose(t, path)
		for name, svc := range f.Services {
			if !isAppService(svc) {
				continue
			}
			checked++
			raw, ok := svc["stop_grace_period"].(string)
			if !assert.True(t, ok, "%s: service %s has no stop_grace_period", path, name) {
				continue
			}
			d, err := time.ParseDuration(raw)
			if assert.NoError(t, err, "%s: service %s", path, name) {
				assert.GreaterOrEqual(t, d, minStopGrace, "%s: service %s stop_grace_period %s", path, name, raw)
			}
		}
	}
	// Защита от пустого прогона: признак сервиса приложения перестал совпадать.
	require.GreaterOrEqual(t, checked, 9, "app services not found in compose files")
}
