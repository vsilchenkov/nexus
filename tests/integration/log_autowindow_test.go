//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/platform/clickhouse"
	"nexus/internal/platform/logging"
	webch "nexus/internal/web/adapter/out/clickhouse"
	"nexus/internal/web/usecase"
	"nexus/internal/web/usecase/port"
)

// autoWindowNodeRepo — минимальный NodeRepo для usecase: один узел с заданной
// таблицей. Полный репозиторий тут не нужен, а PostgreSQL поднимать ради одного
// Get — лишний контейнер.
type autoWindowNodeRepo struct {
	port.NodeRepo
	node *domain.Node
}

func (r *autoWindowNodeRepo) Get(context.Context, string) (*domain.Node, error) {
	return r.node, nil
}

// TestClickHouse_AutoWindow_E2E — §77.2: автоокно сужает чтение, но не меняет
// ни выдачу, ни признак конца истории.
//
// Главный риск фичи — соврать «записей больше нет», когда они просто вне узкого
// окна: фронт считает историю исчерпанной по недобору страницы
// (items.length < limit, §72.2). Поэтому оба подтеста ниже проверяют не
// скорость, а именно ЧЕСТНОСТЬ выдачи: узел, у которого свежих записей нет
// вовсе, обязан отдать полную страницу из старой истории.
func TestClickHouse_AutoWindow_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_autowindow"
	createNodeLogTable(t, ctx, conn, table)

	// История узла: 5 000 записей с шагом в минуту, заканчивается СИЛЬНО раньше
	// «сейчас» (полгода назад). Ни одно из окон 1ч..90д от текущего момента
	// записей не увидит — именно этот случай ломал бы наивную реализацию.
	const (
		total   = 5_000
		stepSec = 60
		baseTS  = "2026-01-10 00:00:00"
	)
	insertSyntheticLogs(t, ctx, conn, table, total, stepSec, baseTS)

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	node := &domain.Node{
		ID:              "n-auto",
		ClickHouseTable: table,
		Status:          domain.NodeStatusEnabled,
	}
	uc := usecase.NewLogsUsecase(reader, &autoWindowNodeRepo{node: node}, logging.NewNoop())

	t.Run("старая история находится с первой страницы", func(t *testing.T) {
		recs, err := uc.Search(ctx, node.ID, "", port.LogQuery{Limit: 50})
		require.NoError(t, err)
		require.Len(t, recs, 50,
			"страница обязана быть полной: недобор фронт трактует как конец истории (§72.2)")
	})

	t.Run("выдача совпадает с запросом без автоокна", func(t *testing.T) {
		// Прямой Search без автоокна — эталон.
		want := searchIDs(t, ctx, reader, port.LogQuery{Table: table, NodeID: "", Limit: 50})
		recs, err := uc.Search(ctx, node.ID, "", port.LogQuery{Limit: 50})
		require.NoError(t, err)
		got := make([]string, 0, len(recs))
		for _, r := range recs {
			got = append(got, r.ID)
		}
		require.Equal(t, want, got, "автоокно обязано быть прозрачным для результата и порядка")
	})

	t.Run("скролл проходит всю историю без ложного конца", func(t *testing.T) {
		seen := map[string]bool{}
		q := port.LogQuery{Limit: 50}
		// Запас к total/limit: лишние итерации ничего не стоят, а нехватка
		// маскировала бы настоящий ложный конец истории.
		maxPages := total/q.Limit + 5
		for page := range maxPages {
			recs, err := uc.Search(ctx, node.ID, "", q)
			require.NoError(t, err)
			if len(recs) < q.Limit {
				// Конец истории допустим только когда прочитано всё.
				for _, r := range recs {
					seen[r.ID] = true
				}
				require.Equal(t, total, len(seen),
					"страница %d недобрала — но история ещё не прочитана целиком", page)
				return
			}
			for _, r := range recs {
				require.False(t, seen[r.ID], "страница %d вернула дубль %s", page, r.ID)
				seen[r.ID] = true
			}
			oldest := recs[len(recs)-1]
			q.UntilMs = oldest.DateRequest.UnixMilli()
			q.BeforeID = oldest.ID
		}
		require.Equal(t, total, len(seen), "скролл обязан дойти до конца истории")
	})

	t.Run("пользовательский from отключает автоокно", func(t *testing.T) {
		base, err := time.Parse("2006-01-02 15:04:05", baseTS)
		require.NoError(t, err)
		// Окно, в котором записей заведомо нет (до начала истории): автоокно
		// расширять его не имеет права — пользователь задал границу сам.
		recs, err := uc.Search(ctx, node.ID, "", port.LogQuery{
			Limit:   50,
			SinceMs: base.Add(-48 * time.Hour).UnixMilli(),
			UntilMs: base.Add(-24 * time.Hour).UnixMilli(),
		})
		require.NoError(t, err)
		require.Empty(t, recs, "заданный пользователем период обязан соблюдаться дословно")
	})
}

// TestClickHouse_ScrollScale_E2E — §77.5: замер скролла на большом объёме.
//
// По умолчанию 1 млн строк (минуты на прогон). Боевой масштаб задаётся
// переменной окружения: LOG_SCALE_ROWS=50000000 go test -tags integration
// -run TestClickHouse_ScrollScale_E2E ./tests/integration/ (см. DEVELOPMENT.md).
// В CI не гоняется — тест пропускается без LOG_SCALE_RUN=1.
func TestClickHouse_ScrollScale_E2E(t *testing.T) {
	if os.Getenv("LOG_SCALE_RUN") != "1" {
		t.Skip("масштабный замер: включается LOG_SCALE_RUN=1 (см. DEVELOPMENT.md)")
	}
	rows := 1_000_000
	if v := os.Getenv("LOG_SCALE_ROWS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, "LOG_SCALE_ROWS")
		rows = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	conn, _, cleanup := startClickHouse(t, ctx)
	defer cleanup()

	const table = "nexus_default.test_scale"
	createNodeLogTable(t, ctx, conn, table)
	t0 := time.Now()
	insertSyntheticLogs(t, ctx, conn, table, rows, 1, "2026-01-01 00:00:00")
	t.Logf("сид: %d строк за %s", rows, time.Since(t0).Round(time.Second))

	reader := webch.NewLogReader(clickhouse.StaticProvider(conn), logging.NewNoop())
	node := &domain.Node{ID: "n-scale", ClickHouseTable: table, Status: domain.NodeStatusEnabled}
	uc := usecase.NewLogsUsecase(reader, &autoWindowNodeRepo{node: node}, logging.NewNoop())

	// Эталон «как было до §77.2»: тот же запрос напрямую в адаптер, без автоокна.
	direct := time.Now()
	_, err := reader.Search(ctx, port.LogQuery{Table: table, Limit: 50})
	require.NoError(t, err)
	directMs := time.Since(direct).Milliseconds()

	const pages = 20
	durations := make([]int64, 0, pages)
	q := port.LogQuery{Limit: 50}
	for range pages {
		start := time.Now()
		recs, err := uc.Search(ctx, node.ID, "", q)
		require.NoError(t, err)
		durations = append(durations, time.Since(start).Milliseconds())
		require.Len(t, recs, 50)
		oldest := recs[len(recs)-1]
		q.UntilMs = oldest.DateRequest.UnixMilli()
		q.BeforeID = oldest.ID
	}
	var sum, worst int64
	for _, d := range durations {
		sum += d
		worst = max(worst, d)
	}
	t.Logf("скролл %d страниц по 50 на %d строк: avg=%d мс, max=%d мс; без автоокна=%d мс",
		pages, rows, sum/int64(len(durations)), worst, directMs)
}

// insertSyntheticLogs — быстрый сид таблицы логов одним INSERT ... SELECT FROM
// numbers. Батч-writer на таких объёмах упирается во flush и делает тест
// медленным и шатким (грабля §72.4). date_create == UTC-день date_request —
// инвариант write-path Nexus.
func insertSyntheticLogs(
	t *testing.T, ctx context.Context, conn chdriver.Conn,
	table string, total, stepSec int, baseTS string,
) {
	t.Helper()
	require.NoError(t, conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s SELECT
			concat('s-', toString(number)), 'request', 'POST',
			'https://example.com/upstream', 'POST', '', '', '',
			200, '',
			toDate(toDateTime('%s') + number*%d),
			toDateTime('%s') + number*%d,
			toDateTime('%s') + number*%d,
			1, 1,
			repeat('a', 32), repeat('b', 32), '', '', '', 1, '[]', '', 0, 0
		FROM numbers(%d)`,
		table, baseTS, stepSec, baseTS, stepSec, baseTS, stepSec, total)))
	waitRows(t, ctx, conn, table, uint64(total))
}
