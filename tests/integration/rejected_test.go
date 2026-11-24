//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	pgpf "nexus/internal/platform/pg"
	"nexus/internal/receiver/adapter/out/rejectlog"
	pgrepo "nexus/internal/web/adapter/out/postgres"
	"nexus/internal/web/usecase/port"
)

// Журнал отказов на входе (§94) на реальном PostgreSQL: сложение счётчиков от
// нескольких экземпляров Receiver, вытеснение сверх лимитов, снятие отметки
// «разобрано» новым отказом, чистка по сроку и полное выключение журнала.
//
// Именно эти инварианты юнит-тестами не проверяются: они живут в SQL (UPSERT с
// LEAST/GREATEST, DELETE … NOT IN, каскады FK), а не в Go.

func rejectedKey(slug, path string) domain.RejectedGroupKey {
	k := domain.RejectedGroupKey{
		TeamSlug: slug, NodePath: path,
		Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST",
	}
	k.Normalize()
	return k
}

func rejectedAgg(k domain.RejectedGroupKey, count int64, first, last time.Time,
	clients []domain.RejectedClient, samples []domain.RejectedSample,
) domain.RejectedAggregate {
	return domain.RejectedAggregate{
		Key: k, Status: 404, Count: count,
		FirstSeen: first, LastSeen: last, Clients: clients, Samples: samples,
	}
}

func rejectedClient(ip string, count int64, at time.Time) domain.RejectedClient {
	return domain.RejectedClient{IP: ip, Count: count, FirstSeen: at, LastSeen: at, UserAgent: "axios/1.6"}
}

func rejectedSample(ip string, at time.Time, path string) domain.RejectedSample {
	return domain.RejectedSample{
		At: at, ClientIP: ip, HTTPMethod: "POST", RawPath: path, Status: 404,
		BodyBytes: 128, Headers: map[string]string{"User-Agent": "axios/1.6"},
	}
}

// TestRejectedFlush_MergesReplicas_E2E: два экземпляра Receiver пишут в одну
// группу без всякой синхронизации между собой — счётчики складываются, границы
// времени сводятся LEAST/GREATEST.
//
// Без этого «последняя запись выигрывает» двигала бы last_seen назад, когда
// пачки приходят не в том порядке, в каком случились отказы.
func TestRejectedFlush_MergesReplicas_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())

	t0 := time.Now().UTC().Truncate(time.Second)
	k := rejectedKey("default", "telephony")

	// Реплика A: 5 отказов от одного клиента.
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 5, t0.Add(-time.Hour), t0.Add(-30*time.Minute),
			[]domain.RejectedClient{rejectedClient("10.0.0.1", 5, t0.Add(-30*time.Minute))},
			[]domain.RejectedSample{rejectedSample("10.0.0.1", t0.Add(-30*time.Minute), "/api/v1/telephony")}),
	}))

	// Реплика B приезжает ПОЗЖЕ, но со СТАРШИМ временем: first_seen должен
	// уехать назад, last_seen — остаться прежним.
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 3, t0.Add(-2*time.Hour), t0.Add(-90*time.Minute),
			[]domain.RejectedClient{rejectedClient("10.0.0.2", 3, t0.Add(-90*time.Minute))},
			nil),
	}))

	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 1, "обе пачки — одна группа")

	g := groups[0]
	assert.Equal(t, int64(8), g.Count, "счётчики складываются, а не перезаписываются")
	assert.Equal(t, t0.Add(-2*time.Hour), g.FirstSeen.UTC())
	assert.Equal(t, t0.Add(-30*time.Minute), g.LastSeen.UTC(), "last_seen не уезжает назад")
	assert.Equal(t, int32(2), g.Clients, "денормализованный счётчик клиентов обновлён")
	// default-команда существует в схеме — join по слогу её находит.
	assert.NotEmpty(t, g.TeamID)
	assert.NotEmpty(t, g.TeamName)

	clients, err := reader.ListRejectedClients(ctx, g.ID, 0)
	require.NoError(t, err)
	assert.Len(t, clients, 2)
}

// TestRejectedFlush_KeepsPtrHostAndCaps_E2E: PTR-имя не затирается пустым
// значением (резолв асинхронный), а число клиентов и сэмплов ограничено.
func TestRejectedFlush_KeepsPtrHostAndCaps_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)
	k := rejectedKey("default", "telephony")

	// Первая пачка: имя уже известно.
	named := rejectedClient("10.0.0.1", 1, now)
	named.Host = "crm-app01.example.ru"
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 1, now, now, []domain.RejectedClient{named}, nil),
	}))

	// Вторая пачка того же клиента БЕЗ имени (кеш PTR ещё не прогрет на другой
	// реплике). Имя обязано сохраниться.
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 1, now, now, []domain.RejectedClient{rejectedClient("10.0.0.1", 1, now)}, nil),
	}))

	clients, err := reader.ListRejectedClients(ctx, mustRejectedGroupID(t, ctx, reader), 0)
	require.NoError(t, err)
	require.Len(t, clients, 1)
	assert.Equal(t, "crm-app01.example.ru", clients[0].Host, "пустое имя не затирает известное")
	assert.Equal(t, int64(2), clients[0].Count)

	// Переполнение: больше лимита клиентов и сэмплов в одной пачке.
	var many []domain.RejectedClient
	var samples []domain.RejectedSample
	for i := range domain.RejectedMaxClientsPerGroup + 10 {
		at := now.Add(time.Duration(i) * time.Second)
		many = append(many, rejectedClient(fmt.Sprintf("10.1.0.%d", i), 1, at))
		samples = append(samples, rejectedSample("10.1.0.1", at, fmt.Sprintf("/api/v1/telephony?n=%d", i)))
	}
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, int64(len(many)), now, now.Add(time.Minute), many, samples),
	}))

	gid := mustRejectedGroupID(t, ctx, reader)
	clients, err = reader.ListRejectedClients(ctx, gid, 1000)
	require.NoError(t, err)
	assert.Len(t, clients, domain.RejectedMaxClientsPerGroup, "клиенты вытесняются сверх лимита")

	stored, err := reader.ListRejectedSamples(ctx, gid, 1000)
	require.NoError(t, err)
	assert.Len(t, stored, domain.RejectedMaxSamplesPerGroup, "сэмплы живут кольцом")

	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, int32(domain.RejectedMaxClientsPerGroup), groups[0].Clients,
		"денормализованный счётчик считает оставшихся, а не всех виденных")
}

// TestRejectedResolve_ClearedByNewReject_E2E (§94.6): новый отказ снимает
// отметку «разобрано» — иначе закрытая группа скрывала бы возобновившуюся
// проблему.
func TestRejectedResolve_ClearedByNewReject_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)
	k := rejectedKey("default", "telephony")

	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 1, now, now, []domain.RejectedClient{rejectedClient("10.0.0.1", 1, now)}, nil),
	}))
	gid := mustRejectedGroupID(t, ctx, reader)
	require.NoError(t, reader.ResolveRejected(ctx, gid, "operator", now))

	// Разобранная группа скрыта из выдачи по умолчанию.
	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	assert.Empty(t, groups)
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	require.NotNil(t, groups[0].ResolvedAt)
	assert.Equal(t, "operator", groups[0].ResolvedBy)

	// Новый отказ — отметка снята, группа снова видна.
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(k, 1, now.Add(time.Minute), now.Add(time.Minute),
			[]domain.RejectedClient{rejectedClient("10.0.0.1", 1, now.Add(time.Minute))}, nil),
	}))
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Nil(t, groups[0].ResolvedAt, "новый отказ снимает отметку «разобрано»")
}

// TestRejectedRetention_DeleteAndPurge_E2E (§94.5): чистка режет по last_seen и
// уносит клиентов с сэмплами каскадом; срок 0 вычищает журнал целиком.
func TestRejectedRetention_DeleteAndPurge_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)

	old := rejectedKey("default", "old-path")
	fresh := rejectedKey("default", "fresh-path")
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		// Старая группа: появилась давно и давно молчит.
		rejectedAgg(old, 3, now.AddDate(0, 0, -40), now.AddDate(0, 0, -35),
			[]domain.RejectedClient{rejectedClient("10.0.0.1", 3, now.AddDate(0, 0, -35))},
			[]domain.RejectedSample{rejectedSample("10.0.0.1", now.AddDate(0, 0, -35), "/api/v1/old-path")}),
		// Живая группа: появилась ещё раньше, но отказы идут сейчас — по
		// first_seen её бы срезало, по last_seen она обязана остаться.
		rejectedAgg(fresh, 2, now.AddDate(0, 0, -60), now,
			[]domain.RejectedClient{rejectedClient("10.0.0.2", 2, now)}, nil),
	}))

	n, err := reader.DeleteRejectedOlderThan(ctx, now.AddDate(0, 0, -30))
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "fresh-path", groups[0].NodePath, "живая группа переживает чистку")

	// Каскад: клиенты и сэмплы удалённой группы ушли вместе с ней.
	var orphanClients, orphanSamples int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM rejected_clients c LEFT JOIN rejected_groups g ON g.id = c.group_id WHERE g.id IS NULL),
		(SELECT count(*) FROM rejected_samples s LEFT JOIN rejected_groups g ON g.id = s.group_id WHERE g.id IS NULL)`).
		Scan(&orphanClients, &orphanSamples))
	assert.Zero(t, orphanClients)
	assert.Zero(t, orphanSamples)

	// Срок 0 — журнал выключен: вычищается всё.
	n, err = reader.PurgeRejected(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{IncludeResolved: true})
	require.NoError(t, err)
	assert.Empty(t, groups)
}

// TestRejectedTrim_CapsStoredGroups_E2E (§94.3): число групп в хранилище
// ограничено — сканер по случайным путям создаёт новую группу на каждую
// попытку, и одного срока хранения против него мало.
func TestRejectedTrim_CapsStoredGroups_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)

	// Десять групп с разным last_seen: чем больше индекс, тем свежее.
	var aggs []domain.RejectedAggregate
	for i := range 10 {
		at := now.Add(time.Duration(i) * time.Minute)
		aggs = append(aggs, rejectedAgg(rejectedKey("default", fmt.Sprintf("scan-%02d", i)), 1, at, at,
			[]domain.RejectedClient{rejectedClient("10.0.0.1", 1, at)},
			[]domain.RejectedSample{rejectedSample("10.0.0.1", at, "/api/v1/scan")}))
	}
	require.NoError(t, writer.Flush(ctx, aggs))

	deleted, err := reader.TrimRejectedGroups(ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, 7, deleted)

	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 3)
	// Остаются САМЫЕ СВЕЖИЕ: журнал отвечает на вопрос «кто стучится сейчас».
	assert.Equal(t, "scan-09", groups[0].NodePath)
	assert.Equal(t, "scan-07", groups[2].NodePath)

	// Каскад отработал и здесь: сирот не осталось.
	var orphans int
	require.NoError(t, pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM rejected_clients c LEFT JOIN rejected_groups g ON g.id = c.group_id WHERE g.id IS NULL)
		+ (SELECT count(*) FROM rejected_samples s LEFT JOIN rejected_groups g ON g.id = s.group_id WHERE g.id IS NULL)`).
		Scan(&orphans))
	assert.Zero(t, orphans)

	// keep <= 0 — не «удалить всё», а «ничего не делать»: иначе опечатка в
	// вызове стёрла бы журнал целиком.
	deleted, err = reader.TrimRejectedGroups(ctx, 0)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	assert.Len(t, groups, 3)
}

// TestRejectedScope_TeamAndUnknownSlug_E2E (§94.6): фильтр по слогу ограничивает
// выдачу, а группа с несуществующим слогом опознаётся как «команда не найдена».
func TestRejectedScope_TeamAndUnknownSlug_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		rejectedAgg(rejectedKey("default", "telephony"), 1, now, now,
			[]domain.RejectedClient{rejectedClient("10.0.0.1", 1, now)}, nil),
		rejectedAgg(rejectedKey("nosuchteam", "wp-login.php"), 7, now, now,
			[]domain.RejectedClient{rejectedClient("95.88.11.4", 7, now)}, nil),
	}))

	// Скоуп одной команды.
	groups, err := reader.ListRejected(ctx, port.RejectedFilter{TeamSlugs: []string{"default"}})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "telephony", groups[0].NodePath)

	// Пустой не-nil список = «ни одной команды», а не «без ограничения».
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{TeamSlugs: []string{}})
	require.NoError(t, err)
	assert.Empty(t, groups, "пустой скоуп не должен показывать всё подряд")

	// Неопознанный слог: команда не резолвится, и по такому признаку группа
	// находится отдельным фильтром.
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{UnknownTeamOnly: true})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "nosuchteam", groups[0].TeamSlug)
	assert.Empty(t, groups[0].TeamID, "команды с таким слогом нет")

	// Поиск по клиенту: оператор ищет «кто это ходит», зная адрес.
	groups, err = reader.ListRejected(ctx, port.RejectedFilter{Query: "95.88.11"})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, "wp-login.php", groups[0].NodePath)

	// Сводка по тому же фильтру: разные адреса считаются один раз.
	s, err := reader.SummaryRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(8), s.Count)
	assert.Equal(t, int64(2), s.Groups)
	assert.Equal(t, int64(2), s.Clients)
	assert.Equal(t, int64(2), s.Unresolved)
}

// mustRejectedGroupID — id единственной группы в журнале.
func mustRejectedGroupID(t *testing.T, ctx context.Context, reader *pgrepo.RejectedRepoPg) string {
	t.Helper()
	groups, err := reader.ListRejected(ctx, port.RejectedFilter{IncludeResolved: true})
	require.NoError(t, err)
	require.Len(t, groups, 1)
	return groups[0].ID
}

// TestRejectedFlush_GarbageSampleKeepsBatch_E2E (§109.5): отказ с мусором от
// клиента — путь и заголовки в cp1251, X-Request-Id длиннее колонки,
// подделанный X-Forwarded-For — доезжает до PostgreSQL вместе с соседним
// нормальным отказом той же пачки.
//
// Пачка пишется одной транзакцией: до санитизации в коллекторе такой образец
// ронял её ошибкой 22021/22001/22P02, и терялись отказы всех групп за интервал
// сброса. Юнит-тест коллектора этого не доказывает — что примет колонка,
// решает PostgreSQL.
func TestRejectedFlush_GarbageSampleKeepsBatch_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	c := rejectlog.New(rejectlog.NewRepo(pool), rejectlog.Config{FlushInterval: time.Hour}, logging.NewNoop())
	c.SetEnabled(true)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(runCtx) // остановка дописывает накопленное одной пачкой
	}()

	c.Add(rejectlog.Sample{
		Key: domain.RejectedGroupKey{
			TeamSlug: "default", NodePath: "telephony",
			Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST",
		},
		Status: 404, ClientIP: "10.0.0.1", RawPath: "/api/v1/telephony",
	})
	c.Add(rejectlog.Sample{
		Key: domain.RejectedGroupKey{
			TeamSlug: "vika_dev5", NodePath: "\xce\xf0",
			Reason: domain.RejectReasonNodeNotFound, HTTPMethod: "POST",
		},
		Status: 404, ClientIP: "<script>", UserAgent: "1C\xff",
		RawPath:   "/api/v1/vika_dev5/\xce\xf0",
		RequestID: strings.Repeat("r", 200),
		Headers:   map[string]string{"Content-Type": "text/\xce\xf0"},
	})
	stop()
	<-done

	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	groups, err := reader.ListRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	require.Len(t, groups, 2, "мусорный образец не уронил пачку")

	paths := []string{groups[0].NodePath, groups[1].NodePath}
	assert.ElementsMatch(t, []string{"telephony", string(utf8.RuneError)}, paths)
}

// TestRejectedMigration_DeletesUnmaskedBotTokens_E2E (§117.7, миграция 0044):
// накопленные до §117 группы и сэмплы с открытым секретом токена бота
// удаляются, а группы без секрета (в том числе уже маскированные `bot<id>:***`)
// остаются вместе со своими клиентами и сэмплами.
//
// Проверяется на реальном PostgreSQL: регулярное выражение миграции обязано
// совпадать с sensitive.MaskBotTokens, а каскады — с DDL 0040.
func TestRejectedMigration_DeletesUnmaskedBotTokens_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	dir, err := filepath.Abs("../../migrations")
	require.NoError(t, err)
	mg, err := pgpf.NewMigratorFromDSN(pool.Config().ConnString(), dir, logging.NewNoop())
	require.NoError(t, err)
	defer mg.Close()
	latest, err := mg.MaxLocalVersion()
	require.NoError(t, err)
	const before = 43 // схема до 0044 — как у боевой базы до выката §117
	require.NoError(t, mg.Down(int(latest-before)))

	// Данные пишутся SQL'ем схемы 0043, а не писателем журнала: текущий писатель
	// знает колонки более поздних миграций (node_id, §117.3).
	const secret = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	leaked := "tg/bot123456:" + secret + "/sendMessage"
	for i, path := range []string{
		leaked,
		"tg/BOT123456:***/sendMessage",
		// Короткий «секрет» — обычный путь, его MaskBotTokens не трогает.
		"robot1:short",
	} {
		var id string
		require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO rejected_groups (team_slug, node_path, reason, http_method, status, first_seen, last_seen, count, clients)
VALUES ('default', $1, 'node_disabled', 'POST', 404, now(), now(), 1, 1) RETURNING id`, path).Scan(&id))
		_, err := pool.Exec(ctx, `
INSERT INTO rejected_clients (group_id, client_ip, count, first_seen, last_seen)
VALUES ($1, $2::inet, 1, now(), now())`, id, fmt.Sprintf("10.0.0.%d", i+1))
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `
INSERT INTO rejected_samples (group_id, at, client_ip, http_method, raw_path, status, body_bytes, request_id, headers)
VALUES ($1, now(), $2::inet, 'POST', $3, 404, 0, '', '{}'::jsonb)`, id, fmt.Sprintf("10.0.0.%d", i+1), "/api/v1/default/"+path)
		require.NoError(t, err)
	}

	require.NoError(t, mg.Up())

	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	groups, err := reader.ListRejected(ctx, port.RejectedFilter{IncludeResolved: true})
	require.NoError(t, err)
	paths := make([]string, 0, len(groups))
	for _, g := range groups {
		paths = append(paths, g.NodePath)
		assert.NotContains(t, g.NodePath, secret)
	}
	assert.ElementsMatch(t, []string{"tg/BOT123456:***/sendMessage", "robot1:short"}, paths)

	var leftovers int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM rejected_samples WHERE raw_path LIKE '%' || $1 || '%'`, secret).Scan(&leftovers))
	assert.Zero(t, leftovers, "сэмплы с секретом уходят вместе с группой")
	var kept int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rejected_samples`).Scan(&kept))
	assert.Equal(t, 2, kept, "сэмплы оставшихся групп не тронуты")
}

// TestRejectedFlush_KeepsNodeID_E2E (§117.3): узел группы пишется, когда пачка
// его знает, и не затирается пачкой без узла (отказ до резолва в ту же группу).
// В ключ группы узел не входит — новый узел той же группы просто заменяет
// прежний, а не открывает вторую группу.
func TestRejectedFlush_KeepsNodeID_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	writer := rejectlog.NewRepo(pool)
	now := time.Now().UTC().Truncate(time.Second)
	k := rejectedKey("default", "orders/create")
	const nodeA = "11111111-1111-4111-8111-111111111111"
	const nodeB = "22222222-2222-4222-8222-222222222222"

	nodeOf := func() string {
		t.Helper()
		var id *string
		require.NoError(t, pool.QueryRow(ctx, `SELECT node_id::text FROM rejected_groups`).Scan(&id))
		if id == nil {
			return ""
		}
		return *id
	}
	flush := func(nodeID string) {
		t.Helper()
		a := rejectedAgg(k, 1, now, now, nil, nil)
		a.NodeID = nodeID
		require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{a}))
	}

	flush("")
	assert.Empty(t, nodeOf(), "отказ до резолва — узла нет")

	flush(nodeA)
	assert.Equal(t, nodeA, nodeOf())

	flush("")
	assert.Equal(t, nodeA, nodeOf(), "пачка без узла не затирает известный")

	flush(nodeB)
	assert.Equal(t, nodeB, nodeOf(), "узел пересоздан с тем же путём — группа та же, узел новый")

	var groups int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM rejected_groups`).Scan(&groups))
	assert.Equal(t, 1, groups, "узел не входит в ключ группы")
}

// TestRejectedRead_NodeCurrentAndDeleted_E2E (§117.5): чтение отдаёт узел
// группы по его ТЕКУЩЕМУ состоянию (переименование и смена статуса после
// отказа видны), а после удаления узла — признак «удалён» без объекта. Join
// не дублирует строки и не мешает счётчикам.
func TestRejectedRead_NodeCurrentAndDeleted_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, cleanup := startPostgres(t, ctx)
	defer cleanup()

	defaultTeam := resolveDefaultTeamID(t, ctx, pool)
	var nodeID string
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO nodes (path, root_method, target_url, team_id, status)
VALUES ('orders/create', 'request', 'https://example.com/hook', $1, 'disabled') RETURNING id::text`,
		defaultTeam).Scan(&nodeID))

	writer := rejectlog.NewRepo(pool)
	reader := pgrepo.NewRejectedRepoPg(pool, logging.NewNoop())
	now := time.Now().UTC().Truncate(time.Second)
	withNode := rejectedAgg(rejectedKey("default", "orders/create"), 2, now, now, nil, nil)
	withNode.NodeID = nodeID
	require.NoError(t, writer.Flush(ctx, []domain.RejectedAggregate{
		withNode,
		rejectedAgg(rejectedKey("default", "nosuch"), 1, now, now, nil, nil),
	}))

	byPath := func() map[string]*domain.RejectedGroup {
		t.Helper()
		groups, err := reader.ListRejected(ctx, port.RejectedFilter{IncludeResolved: true})
		require.NoError(t, err)
		require.Len(t, groups, 2, "join с узлом не дублирует группы")
		out := map[string]*domain.RejectedGroup{}
		for _, g := range groups {
			out[g.NodePath] = g
		}
		return out
	}

	// Узел переименован и включён после отказа — чтение показывает текущее.
	_, err := pool.Exec(ctx, `UPDATE nodes SET path = 'orders/create-v2', status = 'enabled' WHERE id = $1::uuid`, nodeID)
	require.NoError(t, err)
	got := byPath()
	g := got["orders/create"]
	require.NotNil(t, g.Node)
	assert.Equal(t, nodeID, g.Node.ID)
	assert.Equal(t, "orders/create-v2", g.Node.Path)
	assert.Equal(t, domain.NodeStatusEnabled, g.Node.Status)
	assert.Equal(t, "default", g.Node.TeamSlug)
	assert.NotEmpty(t, g.Node.TeamName)
	assert.False(t, g.NodeDeleted)
	assert.Nil(t, got["nosuch"].Node, "отказ до резолва — без узла")
	assert.False(t, got["nosuch"].NodeDeleted)

	one, err := reader.GetRejected(ctx, g.ID)
	require.NoError(t, err)
	require.NotNil(t, one.Node, "карточка тоже несёт узел")

	// Узел удалён — группа остаётся, узел помечен удалённым.
	_, err = pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1::uuid`, nodeID)
	require.NoError(t, err)
	got = byPath()
	assert.Nil(t, got["orders/create"].Node)
	assert.True(t, got["orders/create"].NodeDeleted)

	s, err := reader.SummaryRejected(ctx, port.RejectedFilter{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), s.Groups)
}
