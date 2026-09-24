//go:build integration

package integration

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
	rnodecache "nexus/internal/receiver/adapter/out/nodecache"
	pgrepo "nexus/internal/web/adapter/out/postgres"
)

// receiverCacheSkippedFields — поля domain.Node, которые кеш узла Receiver'а
// (nodecache.Reader) НАМЕРЕННО не читает, с причиной. Всё, чего здесь нет,
// обязано доехать от Web до Receiver'а без потерь.
//
// Добавил поле в domain.Node — реши одно из двух: либо колонка попадает в
// SELECT/Scan Receiver'а (и фикстура ниже получает ненулевое значение), либо
// поле вписывается сюда с причиной. Молчаливого третьего варианта нет: сторож
// упадёт на нулевом значении в фикстуре.
func receiverCacheSkippedFields() map[string]string {
	return map[string]string{
		"ClickHouseTemplateID":    "шаблон таблицы — управление схемой в Web, маршрутизации не нужен",
		"ClickHouseRetentionDays": "retention применяет housekeeping Sender'а",
		"ExternalTable":           "флаг управления таблицей (§64), Receiver таблиц не трогает",
		"DLQTTLSeconds":           "DLQ разбирает Sender, Receiver в DLQ не пишет",
		"DLQRetryDelaySeconds":    "DLQ разбирает Sender, Receiver в DLQ не пишет",
		"TeamSlug":                "слог Receiver берёт из резолва адреса (§101.4); Web его тоже не заполняет",
		"GroupID":                 "UI-группировка (§99), в маршрутизации не участвует",
		"RMQHost":                 "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQPort":                 "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQVHost":                "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQUser":                 "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQPassword":             "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQQueue":                "pull-узлы читает NodeLister, а не HTTP-кеш",
		"RMQUseTLS":               "pull-узлы читает NodeLister, а не HTTP-кеш",
		"PullIntervalSec":         "pull-узлы читает NodeLister, а не HTTP-кеш",
		"PullBatchSize":           "pull-узлы читает NodeLister, а не HTTP-кеш",
		"PullPrefetch":            "pull-узлы читает NodeLister, а не HTTP-кеш",
		"Comment":                 "UI-описание (§29)",
		"CreatedBy":               "автор узла (§63) — только для показа в Web",
		"UpdatedBy":               "автор узла (§63) — только для показа в Web",
	}
}

// TestNodeCache_ReceiverReadsAllFieldsFromPostgres — сторож §107.1: кеш
// Receiver'а при промахе Redis читает узел из PostgreSQL СВОИМ урезанным
// SELECT'ом, и забытая в нём колонка молча превращается в zero value.
//
// Промах — штатная ситуация, а не редкость: после каждого сохранения узла
// Receiver по событию §57 удаляет ключ Redis, и первое же чтение идёт в PG.
// Так уже терялись колонки breaker'а (§81.9) и входящей авторизации по
// query-параметру (§41): узел с токеном в `?api_key=` после любой правки
// начинал искать его в заголовке Authorization.
//
// Эталон — Web-репозиторий (он читает все колонки). Проверяются и чтение из
// PG, и повторное чтение из Redis после write-back (JSON-круг кеша).
func TestNodeCache_ReceiverReadsAllFieldsFromPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, pgCleanup := startPostgres(t, ctx)
	defer pgCleanup()
	rdb, redisCleanup := startRedis(t, ctx)
	defer redisCleanup()

	cipher, err := crypto.NewCipher(testEncryptionKey)
	require.NoError(t, err)
	logger := logging.NewNoop()

	node := nonDefaultRequestNode(resolveDefaultTeamID(t, ctx, pool))
	require.NoError(t, node.Validate(), "фикстура обязана быть узлом, который пропустил бы Web")
	require.NoError(t, pgrepo.NewNodeRepoPg(pool, cipher, logger).Create(ctx, node))
	want, err := pgrepo.NewNodeRepoPg(pool, cipher, logger).Get(ctx, node.ID)
	require.NoError(t, err)
	requireFixtureCoversFields(t, want)

	reader := rnodecache.New(rdb, pool, cipher, time.Minute, logger)
	// Узел записан мимо Web-кеша, но Invalidate делает промах явным — ровно
	// то, что происходит на бою после сохранения узла.
	require.NoError(t, reader.Invalidate(ctx, domain.DefaultTeamSlug, node.Path))

	fromPG, err := reader.Get(ctx, domain.DefaultTeamSlug, node.Path)
	require.NoError(t, err)
	assertNodeFieldsEqual(t, "postgres", want, fromPG)

	// Write-back асинхронный: ждём ключ, затем читаем уже из Redis.
	key := "node:" + domain.DefaultTeamSlug + ":" + node.Path
	require.Eventually(t, func() bool {
		n, err := rdb.Exists(ctx, key).Result()
		return err == nil && n == 1
	}, 5*time.Second, 20*time.Millisecond, "write-back в Redis не случился")

	fromRedis, err := reader.Get(ctx, domain.DefaultTeamSlug, node.Path)
	require.NoError(t, err)
	assertNodeFieldsEqual(t, "redis", want, fromRedis)
}

// nonDefaultRequestNode — sync-узел, у которого КАЖДОЕ читаемое Receiver'ом
// поле отличается от zero value: иначе забытая колонка дала бы тот же ноль и
// сторож ничего бы не заметил.
func nonDefaultRequestNode(teamID string) *domain.Node {
	return &domain.Node{
		Path:                      "guard/all_fields",
		RootMethod:                domain.RootMethodRequest,
		IncomingMethod:            domain.HTTPMethodGET, // SSEDecline требует GET или ANY (§107.2)
		OutgoingMethod:            domain.HTTPMethodDELETE,
		URLMode:                   domain.URLModeStatic,
		TargetURL:                 "http://upstream.example/api",
		URLParamName:              "target",
		URLAllowedHosts:           []string{"upstream.example"},
		PathPassthrough:           true,
		SSEDecline:                true,
		AuthType:                  domain.AuthTypeBasic,
		AuthCredentials:           "user:secret",
		AuthDynamicSource:         domain.AuthDynSourceHeader,
		AuthDynamicField:          "X-Upstream-Auth",
		AuthDynamicStripPrefix:    "Bearer ",
		IncomingAuthType:          domain.IncomingAuthTypeToken,
		IncomingAuthCredentials:   "incoming-token",
		IncomingAuthDynamicSource: domain.IncomingAuthSourceQuery,
		IncomingAuthDynamicField:  "api_key",
		WebhookSignatureHeader:    "X-Signature",
		WebhookSignaturePrefix:    "sha256=",
		ForwardHeaders:            []string{"X-Trace-Id"},
		TimeoutMs:                 4321,
		RetryCount:                2,
		RetryBackoffMs:            150,
		ClickHouseTable:           "nexus.guard_all_fields",
		CircuitBreakerThreshold:   7,
		CircuitBreakerCooldownSec: 45,
		BreakerIgnoreTimeouts:     true,
		Status:                    domain.NodeStatusPaused,
		TeamID:                    teamID,
		LogRequestBody:            true,
		LogResponseBody:           true,
		LogHeaders:                true,
		LoggingEnabled:            true,
		MaxBodySizeEnabled:        true,
		MaxBodySize:               777,
		// Не читаются Receiver'ом, но CHECK таблицы требует значения в диапазоне.
		DLQTTLSeconds:        86_400,
		DLQRetryDelaySeconds: 300,
		AsyncAck: &ackspec.Spec{
			Version:     ackspec.Version,
			ContentType: ackspec.ContentTypeJSON,
			Body:        `{"ok": true}`,
			OnError:     ackspec.OnErrorDefault,
		},
	}
}

// requireFixtureCoversFields — каждое непропускаемое поле эталона ненулевое, а
// список пропусков не ссылается на исчезнувшие поля.
func requireFixtureCoversFields(t *testing.T, want *domain.Node) {
	t.Helper()
	skipped := receiverCacheSkippedFields()
	v := reflect.ValueOf(*want)
	seen := make(map[string]bool, v.NumField())
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		seen[name] = true
		if _, skip := skipped[name]; skip {
			continue
		}
		require.Falsef(t, v.Field(i).IsZero(),
			"поле %s в фикстуре нулевое — задайте ненулевое значение в nonDefaultRequestNode "+
				"или впишите поле в receiverCacheSkippedFields с причиной", name)
	}
	for name := range skipped {
		require.Truef(t, seen[name], "receiverCacheSkippedFields: поля %s больше нет в domain.Node", name)
	}
}

// assertNodeFieldsEqual — поле за полем, чтобы отчёт называл забытую колонку.
// Время сравнивается через Equal: после JSON-круга Redis меняется Location.
func assertNodeFieldsEqual(t *testing.T, source string, want, got *domain.Node) {
	t.Helper()
	skipped := receiverCacheSkippedFields()
	wv, gv := reflect.ValueOf(*want), reflect.ValueOf(*got)
	for i := range wv.NumField() {
		name := wv.Type().Field(i).Name
		if _, skip := skipped[name]; skip {
			continue
		}
		w, g := wv.Field(i).Interface(), gv.Field(i).Interface()
		if wt, ok := w.(time.Time); ok {
			assert.Truef(t, wt.Equal(g.(time.Time)), "%s: поле %s: want %v, got %v", source, name, w, g)
			continue
		}
		assert.Equalf(t, w, g, "%s: поле %s не доехало до Receiver'а", source, name)
	}
}
