//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/keyrotate"
	"nexus/internal/platform/logging"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// §90.4: ротация ключа обязана покрывать ВСЕ зашифрованные данные — секретные
// колонки nodes (включая rmq_password, который раньше забывали) и секреты
// app_settings. После прогона всё читается новым ключом, повтор ничего не
// меняет, dry-run не пишет.

func cipherFromByte(t *testing.T, b byte) (*crypto.Cipher, string) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	key := base64.StdEncoding.EncodeToString(raw)
	c, err := crypto.NewCipher(key)
	require.NoError(t, err)
	return c, key
}

// seedRotationFixture заводит команду, узел с тремя секретными колонками и
// настройки приложения — всё зашифрованное ключом oldC.
func seedRotationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, oldC *crypto.Cipher) string {
	t.Helper()

	var teamID string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT id::text FROM teams WHERE slug = 'default'").Scan(&teamID))

	nodeRepo := pgrepo.NewNodeRepoPg(pool, oldC, logging.NewNoop())
	node := &domain.Node{
		TeamID:     teamID,
		Path:       "rotate-fixture",
		RootMethod: domain.RootMethodRequest,
		URLMode:    domain.URLModeStatic,
		TargetURL:  "https://example.com/hook",

		AuthCredentials:         "outgoing-secret",
		IncomingAuthCredentials: "incoming-secret",
		RMQPassword:             "rmq-secret",
	}
	// Репозиторий вызывается напрямую, минуя usecase, поэтому дефолты полей
	// (источник динамической авторизации и пр.) нужно проставить самим — иначе
	// вставку отклоняют CHECK-констрейнты таблицы.
	node.SetDefaults()
	require.NoError(t, nodeRepo.Create(ctx, node))
	require.NotEmpty(t, node.ID)

	appRepo := pgrepo.NewAppSettingsRepoPg(pool, oldC, logging.NewNoop())
	require.NoError(t, appRepo.Update(ctx, &domain.AppSettings{
		Sentry:     domain.SentrySettings{DSN: new("https://key@sentry.example.com/52")},
		ClickHouse: domain.ClickHouseSettings{Password: new("ch-secret"), Host: new("ch1")},
		Mail:       domain.MailSettings{Password: new("smtp-secret")},
	}))

	return node.ID
}

func TestRotateKey_NodesAndAppSettings_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	oldC, _ := cipherFromByte(t, 1)
	newC, _ := cipherFromByte(t, 2)
	nodeID := seedRotationFixture(t, ctx, pool, oldC)

	nodeStats, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	appStats, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)

	assert.Equal(t, 3, nodeStats.Reencrypted, "три секретные колонки узла, включая rmq_password")
	assert.Equal(t, 3, appStats.Reencrypted, "три заданных секрета app_settings")

	// Новый ключ читает всё, что было записано старым.
	newNodeRepo := pgrepo.NewNodeRepoPg(pool, newC, logging.NewNoop())
	got, err := newNodeRepo.Get(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, "outgoing-secret", got.AuthCredentials)
	assert.Equal(t, "incoming-secret", got.IncomingAuthCredentials)
	assert.Equal(t, "rmq-secret", got.RMQPassword, "rmq_password обязан пережить ротацию")

	newAppRepo := pgrepo.NewAppSettingsRepoPg(pool, newC, logging.NewNoop())
	settings, err := newAppRepo.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://key@sentry.example.com/52", *settings.Sentry.DSN)
	assert.Equal(t, "ch-secret", *settings.ClickHouse.Password)
	assert.Equal(t, "smtp-secret", *settings.Mail.Password)
	assert.Equal(t, "ch1", *settings.ClickHouse.Host, "несекретные поля не тронуты")

	// Повторный прогон — идемпотентность (прерванную ротацию можно повторить).
	nodeStats2, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	appStats2, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 0, nodeStats2.Reencrypted+appStats2.Reencrypted)
	assert.Equal(t, 3, nodeStats2.SkippedAlreadyNew)
	assert.Equal(t, 3, appStats2.SkippedAlreadyNew)
}

// Прогон с OLD_KEY == NEW_KEY работает как разовая миграция исторических
// plaintext-значений: до §90.1 секреты app_settings лежали открытыми.
func TestRotateKey_UpgradesLegacyPlaintext_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	c, _ := cipherFromByte(t, 3)

	// Пишем мимо репозитория — так выглядит боевая строка до обновления.
	_, err := pool.Exec(ctx, `UPDATE app_settings SET value = $1::jsonb WHERE id = 1`,
		`{"sentry":{"dsn":"legacy-dsn"},"clickhouse":{"password":"legacy-ch","host":"ch1"}}`)
	require.NoError(t, err)

	st, err := keyrotate.RotateAppSettings(ctx, pool, c, c, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 2, st.Encrypted, "оба открытых секрета зашифрованы")
	assert.Equal(t, 0, st.Reencrypted)

	var raw string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT value::text FROM app_settings WHERE id = 1").Scan(&raw))
	assert.NotContains(t, raw, "legacy-dsn")
	assert.NotContains(t, raw, "legacy-ch")
	assert.Equal(t, 2, strings.Count(raw, "v1:"))
	assert.Contains(t, raw, "ch1")

	settings, err := pgrepo.NewAppSettingsRepoPg(pool, c, logging.NewNoop()).Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "legacy-dsn", *settings.Sentry.DSN)
	assert.Equal(t, "legacy-ch", *settings.ClickHouse.Password)
}

func TestRotateKey_DryRunDoesNotWrite_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	oldC, _ := cipherFromByte(t, 1)
	newC, _ := cipherFromByte(t, 2)
	nodeID := seedRotationFixture(t, ctx, pool, oldC)

	var before string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT value::text FROM app_settings WHERE id = 1").Scan(&before))
	// Колонки узла пишутся построчно, поэтому их неизменность проверяется
	// отдельно от singleton'а настроек.
	var nodeBefore [3]string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_credentials, incoming_auth_credentials, COALESCE(rmq_password, '')
		   FROM nodes WHERE id = $1::uuid`, nodeID).
		Scan(&nodeBefore[0], &nodeBefore[1], &nodeBefore[2]))

	nodeStats, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, keyrotate.Options{DryRun: true}, logging.NewNoop())
	require.NoError(t, err)
	appStats, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, keyrotate.Options{DryRun: true}, logging.NewNoop())
	require.NoError(t, err)

	assert.Equal(t, 3, nodeStats.Reencrypted, "dry-run отчитывается о планируемых изменениях")
	assert.Equal(t, 3, appStats.Reencrypted)

	var after string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT value::text FROM app_settings WHERE id = 1").Scan(&after))
	assert.Equal(t, before, after, "dry-run не пишет в БД")

	var nodeAfter [3]string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_credentials, incoming_auth_credentials, COALESCE(rmq_password, '')
		   FROM nodes WHERE id = $1::uuid`, nodeID).
		Scan(&nodeAfter[0], &nodeAfter[1], &nodeAfter[2]))
	assert.Equal(t, nodeBefore, nodeAfter, "dry-run не трогает секретные колонки узла")

	// Данные по-прежнему читаются СТАРЫМ ключом.
	settings, err := pgrepo.NewAppSettingsRepoPg(pool, oldC, logging.NewNoop()).Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ch-secret", *settings.ClickHouse.Password)
}

// §90.6: разовая миграция открытых данных (MODE=encrypt) и обратный ход
// (MODE=decrypt) на реальной БД — включая колонки nodes, которые пишутся
// построчно, и singleton app_settings.
func TestKeyRotate_EncryptThenDecrypt_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	c, _ := cipherFromByte(t, 4)
	nodeID := seedRotationFixture(t, ctx, pool, c)

	// Раскладываем данные так, как они выглядят на боевой БД до §90.1: узел с
	// открытыми кредами и настройки с открытыми секретами (пишем мимо
	// репозиториев, иначе они зашифруют на входе).
	_, err := pool.Exec(ctx,
		`UPDATE nodes SET auth_credentials = 'legacy-node-secret' WHERE id = $1::uuid`, nodeID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE app_settings SET value = $1::jsonb WHERE id = 1`,
		`{"sentry":{"dsn":"legacy-dsn"},"clickhouse":{"password":"legacy-ch","host":"ch1"}}`)
	require.NoError(t, err)

	// ── MODE=encrypt: открытое шифруется, уже зашифрованное не трогается ──
	nodeSt, err := keyrotate.RotateNodes(ctx, pool, c, c, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	appSt, err := keyrotate.RotateAppSettings(ctx, pool, c, c, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)

	assert.Equal(t, 1, nodeSt.Encrypted, "открытым лежал один credential узла")
	assert.Equal(t, 2, nodeSt.SkippedAlreadyNew, "две уже зашифрованные колонки пропущены")
	assert.Equal(t, 2, appSt.Encrypted)

	var rawNode, rawApp string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_credentials FROM nodes WHERE id = $1::uuid`, nodeID).Scan(&rawNode))
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT value::text FROM app_settings WHERE id = 1").Scan(&rawApp))
	assert.True(t, strings.HasPrefix(rawNode, "v1:"), "credential узла зашифрован")
	assert.NotContains(t, rawApp, "legacy-dsn")
	assert.NotContains(t, rawApp, "legacy-ch")

	// Повторный прогон ничего не переписывает — двойного шифрования нет.
	nodeSt2, err := keyrotate.RotateNodes(ctx, pool, c, c, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	appSt2, err := keyrotate.RotateAppSettings(ctx, pool, c, c, keyrotate.Options{}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 0, nodeSt2.Encrypted+nodeSt2.Reencrypted)
	assert.Equal(t, 0, appSt2.Encrypted+appSt2.Reencrypted)

	var rawNodeAgain string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_credentials FROM nodes WHERE id = $1::uuid`, nodeID).Scan(&rawNodeAgain))
	assert.Equal(t, rawNode, rawNodeAgain, "значение осталось байт в байт прежним")

	// Данные читаются сервисом.
	repo := pgrepo.NewNodeRepoPg(pool, c, logging.NewNoop())
	node, err := repo.Get(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, "legacy-node-secret", node.AuthCredentials)

	// ── MODE=decrypt: обратный ход ТОЛЬКО для app_settings ──
	//
	// Креды узлов раскрывать нельзя: все три сервиса читают их строгим Decrypt,
	// поэтому plaintext в этих колонках означал бы неработающие узлы. Утилита
	// обязана отказаться, а не «услужливо» расшифровать.
	_, err = keyrotate.RotateNodes(ctx, pool, c, c,
		keyrotate.Options{Decrypt: true}, logging.NewNoop())
	require.ErrorIs(t, err, keyrotate.ErrDecryptNodesForbidden)

	appDec, err := keyrotate.RotateAppSettings(ctx, pool, c, c,
		keyrotate.Options{Decrypt: true}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 2, appDec.Decrypted)

	require.NoError(t, pool.QueryRow(ctx,
		`SELECT auth_credentials FROM nodes WHERE id = $1::uuid`, nodeID).Scan(&rawNode))
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT value::text FROM app_settings WHERE id = 1").Scan(&rawApp))
	assert.True(t, strings.HasPrefix(rawNode, "v1:"), "креды узла остались зашифрованными")
	assert.Contains(t, rawApp, "legacy-dsn", "секреты настроек снова открытым текстом")
	assert.Contains(t, rawApp, "ch1", "несекретные поля целы")

	// Повторный обратный ход безопасен.
	appDec2, err := keyrotate.RotateAppSettings(ctx, pool, c, c,
		keyrotate.Options{Decrypt: true}, logging.NewNoop())
	require.NoError(t, err)
	assert.Equal(t, 0, appDec2.Decrypted)
	assert.Equal(t, 2, appDec2.SkippedPlaintext)

	// Узлы продолжают читаться сервисом, несмотря на прогон обратного хода.
	node, err = repo.Get(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, "legacy-node-secret", node.AuthCredentials)

	// И код §90.1 читает открытые настройки (lazy-upgrade).
	settings, err := pgrepo.NewAppSettingsRepoPg(pool, c, logging.NewNoop()).Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, "legacy-dsn", *settings.Sentry.DSN)
}
