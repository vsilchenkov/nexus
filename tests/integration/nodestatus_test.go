//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/nodestatus"
	webredis "nexus/internal/web/adapter/out/redis"
	webusecase "nexus/internal/web/usecase"
)

// TestNodeStatus_WriterReader_Redis_E2E (§46, §52): Sender-writer пишет исход
// последнего исходящего вызова узла в Redis, Web-reader читает его тем же ключом.
// Проверяет round-trip всех трёх исходов (ok/degraded/down), согласованность
// формата ключа (nexus:node:last_error:id:<node_id>, §101.6) и legacy-совместимость — это и
// есть механизм, который держит runtime-бейдж после рестарта процесса (ключ в
// Redis переживает рестарт, в отличие от §41-гауджа).
func TestNodeStatus_WriterReader_Redis_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	logger := logging.NewNoop()
	// Порог 1 (§52.8) = «любой тяжёлый отказ сразу down»: сценарий проверяет
	// кодировку значения и чтение на стороне Web, а не накопление отказов —
	// с дефолтными 10 первый же down читался бы как degraded и тест проверял
	// бы совсем другое. Порог покрыт своими тестами в platform/nodestatus.
	writer := nodestatus.NewRedisWriter(client, 1, logger) // Sender-сторона
	reader := webredis.NewNodeStatusReaderRedis(client, logger)

	const (
		idFail    = "00000000-0000-4000-8000-00000000f001"
		idFlaky   = "00000000-0000-4000-8000-00000000f002"
		idOK      = "00000000-0000-4000-8000-00000000f003"
		idNever   = "00000000-0000-4000-8000-00000000f004"
		idLegacy  = "00000000-0000-4000-8000-00000000f005"
		idGarbage = "00000000-0000-4000-8000-00000000f006"
	)

	writer.SetLastOutcome(ctx, idFail, domain.NodeOutcomeDown)      // транспорт/5xx
	writer.SetLastOutcome(ctx, idFlaky, domain.NodeOutcomeDegraded) // 3xx/4xx (§50.4)
	writer.SetLastOutcome(ctx, idOK, domain.NodeOutcomeOK)          // 2xx

	got, err := reader.GetLastOutcomes(ctx,
		[]string{idFail, idFlaky, idOK, idNever})
	require.NoError(t, err)

	v, ok := got[idFail]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeDown, v, "транспорт/5xx → down")

	v, ok = got[idFlaky]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeDegraded, v, "4xx → degraded (узел жив)")

	v, ok = got[idOK]
	require.True(t, ok)
	require.Equal(t, domain.NodeOutcomeOK, v, "2xx → ok")

	_, ok = got[idNever]
	require.False(t, ok, "узел без записи не попадает в карту (добор из Prometheus)")

	// Legacy §46: булево "1" старого Sender («любой не-2xx») читается как down —
	// worst case, самоисцелится следующим вызовом узла.
	require.NoError(t,
		client.Set(ctx, nodestatus.Key(idLegacy), "1", time.Hour).Err())
	got, err = reader.GetLastOutcomes(ctx, []string{idLegacy})
	require.NoError(t, err)
	require.Equal(t, domain.NodeOutcomeDown, got[idLegacy],
		"legacy \"1\" → down")

	// Нераспознанное значение — узел пропускается (fallback на Prometheus).
	require.NoError(t,
		client.Set(ctx, nodestatus.Key(idGarbage), "banana", time.Hour).Err())
	got, err = reader.GetLastOutcomes(ctx, []string{idGarbage})
	require.NoError(t, err)
	_, ok = got[idGarbage]
	require.False(t, ok, "мусорное значение не попадает в карту")

	// Перезапись: повторный успешный вызов снимает «Down».
	writer.SetLastOutcome(ctx, idFail, domain.NodeOutcomeOK)
	got, err = reader.GetLastOutcomes(ctx, []string{idFail})
	require.NoError(t, err)
	require.Equal(t, domain.NodeOutcomeOK, got[idFail],
		"перезапись 2xx → больше не Down")

	// Пустой список узлов — пустая карта, без обращения к Redis.
	empty, err := reader.GetLastOutcomes(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

// TestNodeStatus_LegacyMigration_Redis (§101.6): перенос старых ключей исхода по
// пути на ключи по id на настоящем Redis. Unit-тесты гоняют Lua-скрипт в
// miniredis, а сроки ключей (PTTL, SET PX) он воспроизводит не во всём. Путь
// telegram (занят узлами двух команд) резолвер не отдаёт, и он не переносится;
// свежий ключ по id не перезаписывается; повторный старт ничего не воскрешает.
func TestNodeStatus_LegacyMigration_Redis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client, cleanup := startRedis(t, ctx)
	defer cleanup()

	const (
		idBilling = "0b1e2c3d-0000-4000-8000-000000000003"
		idEcho    = "0b1e2c3d-0000-4000-8000-000000000004"
	)
	require.NoError(t, client.Set(ctx, nodestatus.LegacyKey("billing"), "1", 5*time.Hour).Err())
	require.NoError(t, client.Set(ctx, nodestatus.LegacyKey("partner/echo"), "1", 5*time.Hour).Err())
	require.NoError(t, client.Set(ctx, nodestatus.LegacyKey("telegram"), "1", 5*time.Hour).Err())
	// Новый Sender уже успел записать исход узла по id — он свежее старого ключа.
	require.NoError(t, client.Set(ctx, nodestatus.Key(idEcho), "0", time.Hour).Err())

	logger := logging.NewNoop()
	store := webredis.NewNodeStatusReaderRedis(client, logger)
	paths := &migrationPaths{ids: map[string]string{"billing": idBilling, "partner/echo": idEcho}}
	migration := webusecase.NewNodeStatusMigration(store, paths, logger)
	require.NoError(t, migration.Run(ctx))

	got, err := store.GetLastOutcomes(ctx, []string{idBilling, idEcho})
	require.NoError(t, err)
	require.Equal(t, domain.NodeOutcomeDown, got[idBilling], "однозначный путь перенесён")
	require.Equal(t, domain.NodeOutcomeOK, got[idEcho], "ключ по id свежее старого и не перезаписан")
	ttl, err := client.PTTL(ctx, nodestatus.Key(idBilling)).Result()
	require.NoError(t, err)
	require.InDelta(t, (5 * time.Hour).Seconds(), ttl.Seconds(), 60, "срок старого ключа сохранён")
	require.ElementsMatch(t, []string{"billing", "partner/echo", "telegram"}, paths.asked)

	// Ручной сброс защиты (§81.4.2) снимает исход, затем Web перезапускается.
	require.NoError(t, client.Del(ctx, nodestatus.Key(idBilling)).Err())
	require.NoError(t, migration.Run(ctx))
	got, err = store.GetLastOutcomes(ctx, []string{idBilling})
	require.NoError(t, err)
	require.NotContains(t, got, idBilling, "снятый исход не воскресает на рестарте")
}

// migrationPaths — резолвер «путь → id» для однозначных путей. Сам SQL-запрос
// проверяет TestNodeRepoIDsByPaths_E2E на живом PostgreSQL.
type migrationPaths struct {
	ids   map[string]string
	asked []string
}

func (m *migrationPaths) IDsByPaths(_ context.Context, paths []string) (map[string]string, error) {
	m.asked = append(m.asked, paths...)
	out := map[string]string{}
	for _, p := range paths {
		if id, ok := m.ids[p]; ok {
			out[p] = id
		}
	}
	return out, nil
}
