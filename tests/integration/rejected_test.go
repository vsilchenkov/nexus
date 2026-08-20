//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
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
