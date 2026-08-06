//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// sigurAckSpec — боевая форма шаблона §83: эхо максимального logId пакета.
func sigurAckSpec() *ackspec.Spec {
	return &ackspec.Spec{
		Version:     ackspec.Version,
		ContentType: ackspec.ContentTypeJSON,
		Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
		OnError:     ackspec.OnErrorDefault,
	}
}

// TestNodeAsyncAckSpec_Persistence — §83: спека доезжает до PostgreSQL и
// обратно через реальную миграцию 0034.
//
// Сценарий:
//  1. Узел без спеки → в колонке NULL, в домене nil («отвечать как раньше»).
//  2. Update со спекой → спека читается обратно дословно, включая шаблон.
//  3. Смена root_method requestAsync → request НЕ теряет спеку (контракт
//     §83.0: оператор временно переводит узел в sync).
//  4. Update со спекой nil → колонка снова NULL (выключение работает).
func TestNodeAsyncAckSpec_Persistence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	repo := pgrepo.NewNodeRepoPg(pool, cipher, logging.NewNoop())
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	// 1. Узел без спеки.
	n := &domain.Node{
		Path:       "ack/persist",
		RootMethod: domain.RootMethodRequestAsync,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
		TeamID:     defaultTeam,
	}
	n.SetDefaults() // репозиторий вызывается напрямую, мимо usecase
	require.NoError(t, repo.Create(ctx, n))

	got, err := repo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Nil(t, got.AsyncAck, "узел без спеки читается как nil")

	var isNull bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT async_ack_spec IS NULL FROM nodes WHERE id = $1`, n.ID).Scan(&isNull))
	assert.True(t, isNull, "в колонке NULL, а не пустой объект")

	// 2. Включаем спеку.
	withSpec := *got
	withSpec.AsyncAck = sigurAckSpec()
	require.NoError(t, repo.Update(ctx, &withSpec))

	got, err = repo.Get(ctx, n.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AsyncAck, "спека потеряна при чтении")
	assert.Equal(t, sigurAckSpec().Body, got.AsyncAck.Body, "шаблон обязан сохраниться дословно")
	assert.Equal(t, ackspec.ContentTypeJSON, got.AsyncAck.ContentType)
	assert.Equal(t, ackspec.OnErrorDefault, got.AsyncAck.OnError)
	assert.Equal(t, ackspec.Version, got.AsyncAck.Version)

	// Колонка — именно jsonb-объект, а не строка с JSON внутри: иначе CHECK
	// nodes_async_ack_spec_object не сработал бы, а SQL-разбор на бою был бы
	// невозможен.
	var jsonType string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT jsonb_typeof(async_ack_spec) FROM nodes WHERE id = $1`, n.ID).Scan(&jsonType))
	assert.Equal(t, "object", jsonType)

	// 3. Перевод в sync спеку не трогает.
	toSync := *got
	toSync.RootMethod = domain.RootMethodRequest
	require.NoError(t, repo.Update(ctx, &toSync))

	got, err = repo.Get(ctx, n.ID)
	require.NoError(t, err)
	require.NotNil(t, got.AsyncAck, "перевод узла в sync НЕ должен терять шаблон (§83.0)")
	assert.Equal(t, sigurAckSpec().Body, got.AsyncAck.Body)

	// 4. Выключение спеки.
	off := *got
	off.AsyncAck = nil
	require.NoError(t, repo.Update(ctx, &off))

	got, err = repo.Get(ctx, n.ID)
	require.NoError(t, err)
	assert.Nil(t, got.AsyncAck)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT async_ack_spec IS NULL FROM nodes WHERE id = $1`, n.ID).Scan(&isNull))
	assert.True(t, isNull, "выключение шаблона обязано класть NULL")
}

// TestNodeAsyncAckSpec_Constraints — §83: CHECK-ограничения миграции 0034 —
// второй рубеж после domain.Node.Validate. Через Web в колонку кривое не
// попадёт, но ручная правка SQL на бою не должна оставлять узел, на котором
// Receiver споткнётся.
func TestNodeAsyncAckSpec_Constraints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	repo := pgrepo.NewNodeRepoPg(pool, cipher, logging.NewNoop())
	defaultTeam := resolveDefaultTeamID(t, ctx, pool)

	n := &domain.Node{
		Path:       "ack/constraints",
		RootMethod: domain.RootMethodRequestAsync,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",
		TeamID:     defaultTeam,
	}
	n.SetDefaults() // репозиторий вызывается напрямую, мимо usecase
	require.NoError(t, repo.Create(ctx, n))

	t.Run("array is rejected", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`UPDATE nodes SET async_ack_spec = '[1,2,3]'::jsonb WHERE id = $1`, n.ID)
		require.Error(t, err, "не-объект обязан отбиваться CHECK'ом")
		assert.Contains(t, err.Error(), "nodes_async_ack_spec_object")
	})

	t.Run("oversized spec is rejected", func(t *testing.T) {
		// 16 КиБ — потолок колонки (вдвое больше ackspec.MaxTemplateBytes).
		_, err := pool.Exec(ctx, `
UPDATE nodes SET async_ack_spec = jsonb_build_object('body', repeat('x', 20000)) WHERE id = $1`, n.ID)
		require.Error(t, err, "спека больше 16 КиБ обязана отбиваться CHECK'ом")
		assert.Contains(t, err.Error(), "nodes_async_ack_spec_size")
	})

	t.Run("valid spec passes", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
UPDATE nodes SET async_ack_spec = '{"version":1,"content_type":"application/json","body":"{}","on_error":"default"}'::jsonb
WHERE id = $1`, n.ID)
		require.NoError(t, err)
	})
}
