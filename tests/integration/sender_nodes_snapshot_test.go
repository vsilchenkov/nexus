//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	"nexus/internal/sender/adapter/out/nodepg"
)

// TestNodesSnapshot_Fingerprint (§113.3): отпечаток nodes меняется от любого
// изменения строк — включая смену статуса туда-обратно между двумя чтениями,
// INSERT и DELETE — и не меняется от VACUUM FREEZE. На нём держится решение
// «запускать ли раунд прохода очереди паузы».
func TestNodesSnapshot_Fingerprint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	reader := nodepg.New(pool, cipher, logging.NewNoop())

	snap := func() (int, string) {
		t.Helper()
		paused, fp, err := reader.NodesSnapshot(ctx)
		require.NoError(t, err)
		return paused, fp
	}

	paused, empty := snap()
	assert.Zero(t, paused)
	assert.Empty(t, empty, "пустая таблица — пустой отпечаток, не ошибка")

	nodeUC := newAsyncNodeUsecase(t, ctx, pool, cipher)
	createAsyncNode(t, ctx, nodeUC, "fp/a", "http://127.0.0.1:1/a", "test.fp_a", domain.NodeStatusEnabled)
	createAsyncNode(t, ctx, nodeUC, "fp/b", "http://127.0.0.1:1/b", "test.fp_b", domain.NodeStatusPaused)

	paused, afterInsert := snap()
	assert.Equal(t, 1, paused)
	assert.NotEqual(t, empty, afterInsert, "INSERT меняет отпечаток")

	_, again := snap()
	assert.Equal(t, afterInsert, again, "без изменений отпечаток стабилен")

	setNodeStatus(t, ctx, pool, "fp/b", domain.NodeStatusEnabled)
	setNodeStatus(t, ctx, pool, "fp/b", domain.NodeStatusPaused)
	paused, roundTrip := snap()
	assert.Equal(t, 1, paused, "статус вернулся к прежнему")
	assert.NotEqual(t, afterInsert, roundTrip, "смена статуса туда-обратно видна (xmin новый)")

	_, err = pool.Exec(ctx, `VACUUM (FREEZE) nodes`)
	require.NoError(t, err)
	_, frozen := snap()
	assert.Equal(t, roundTrip, frozen, "заморозка не меняет отпечаток — лишних раундов нет")

	_, err = pool.Exec(ctx, `DELETE FROM nodes WHERE path = 'fp/a'`)
	require.NoError(t, err)
	paused, afterDelete := snap()
	assert.Equal(t, 1, paused)
	assert.NotEqual(t, frozen, afterDelete, "DELETE меняет отпечаток")
}
