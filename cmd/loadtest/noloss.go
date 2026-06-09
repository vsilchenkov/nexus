package main

import (
	"context"
	"fmt"
	"regexp"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
)

// tableNameRe — допустимое имя CH-таблицы (db.table или table). Защищает от
// инъекции через флаг --ch-table перед подстановкой в SQL count-запрос.
var tableNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)?$`)

// noLossStableRounds — сколько подряд опросов с неизменным count'ом считаем
// признаком «бэклог разгрёбся» (плато). При таком плато rows<expected — это
// уже реальная потеря, а не лаг доставки.
const noLossStableRounds = 3

// noLossResult — результат сверки «нет потерь» (§10.2) с ClickHouse-логом.
type noLossResult struct {
	enabled  bool
	rows     int64 // строк type IN (requestAsync, RabbitMQAsync) в CH-логе
	expected int64 // async + rmq отправлено (at-least-once-пути)
	ok       bool
	// inconclusive — rows<expected, но потеря НЕ подтверждена: на момент maxWait
	// (или отмены ctx) count всё ещё рос, т.е. бэклог Kafka/RMQ ещё дренировался.
	// At-least-once + Kafka хранит непрочитанное, поэтому это «не успели дослить»,
	// а не «потеряли». Вызывающий трактует как WARN, а не FAIL (см. report.passed).
	inconclusive bool
	err          error
}

// countFn возвращает текущее число подходящих строк в CH-логе.
type countFn func(ctx context.Context) (int64, error)

// pollOutcome — причина остановки pollUntilStable. Разделяет «потеря
// подтверждена» (плато) и «не успели дослить» (maxWait/отмена при растущем count).
type pollOutcome int

const (
	pollReachedExpected pollOutcome = iota // rows>=expected — потерь нет
	pollPlateau                            // count замер ниже expected — реальная потеря
	pollMaxWait                            // истёк maxWait, count ещё рос — inconclusive
	pollCtxDone                            // ctx отменён — inconclusive (или ошибка до 1-го замера)
)

// checkNoLoss сверяет число async/rmq-строк в CH-логе с expected. Колонка type
// хранит RootMethod (send.go), async-consumer пишет requestAsync и для
// RMQ-сообщений (async.go) — поэтому фильтруем оба значения, чтобы sync-строки
// не забивали счётчик.
//
// Доставка async/rmq — at-least-once и асинхронная: после остановки нагрузки
// sender ещё разгребает бэклог Kafka и RMQ→puller→Kafka. Поэтому считаем не
// единожды, а опрашиваем до стабилизации (см. pollUntilStable): растущий count
// = ещё дренируется (ждём), плато ниже expected = реальная потеря. Дубликаты
// (rows>expected) нарушением НЕ считаются. Пустой --ch-addr = пропуск.
func checkNoLoss(ctx context.Context, f flags, expected int64) noLossResult {
	if f.CHAddr == "" {
		return noLossResult{}
	}
	res := noLossResult{enabled: true, expected: expected}
	if !tableNameRe.MatchString(f.CHTable) {
		res.err = fmt.Errorf("invalid --ch-table %q", f.CHTable)
		return res
	}

	conn, err := chgo.Open(&chgo.Options{
		Addr:        []string{f.CHAddr},
		Auth:        chgo.Auth{Username: f.CHUser, Password: f.CHPassword},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		res.err = fmt.Errorf("clickhouse open %s: %w", f.CHAddr, err)
		return res
	}
	defer conn.Close()

	// #nosec G201 -- f.CHTable провалидирован tableNameRe; фильтр — константы.
	query := fmt.Sprintf("SELECT count() FROM %s WHERE type IN ('requestAsync','RabbitMQAsync')", f.CHTable)
	count := func(ctx context.Context) (int64, error) {
		qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		var rows uint64
		if err := conn.QueryRow(qctx, query).Scan(&rows); err != nil {
			return 0, fmt.Errorf("count %s: %w", f.CHTable, err)
		}
		return int64(rows), nil
	}

	fmt.Printf("no-loss: опрашиваем CH до стабилизации (интервал=%s, потолок=%s)...\n",
		f.CHFlushGrace, f.CHNoLossMaxWait)
	rows, outcome, err := pollUntilStable(ctx, count, expected, f.CHFlushGrace, f.CHNoLossMaxWait)
	if err != nil {
		res.err = err
		return res
	}
	res.rows = rows
	res.ok = rows >= expected
	// Потеря ПОДТВЕРЖДЕНА только при плато (pollPlateau): сообщений больше не
	// придёт. Любой другой выход ниже expected (maxWait/отмена при растущем
	// count) — бэклог ещё дренировался → inconclusive, не подтверждённая потеря.
	res.inconclusive = !res.ok && outcome != pollPlateau
	return res
}

// pollUntilStable опрашивает cnt с интервалом interval (ожидание выполняется и
// перед первым опросом — это initial settle) и возвращает последний count + причину
// остановки (pollOutcome) при одном из условий: rows>=expected (pollReachedExpected,
// потерь нет, ранний выход); count не растёт noLossStableRounds опросов подряд
// (pollPlateau — бэклог разгрёбся, ниже expected = реальная потеря); истёк maxWait
// при ещё растущем count (pollMaxWait — не успели дослить); отменён ctx (pollCtxDone).
// Возвращает ошибку только при сбое cnt или отмене до первого успешного замера.
func pollUntilStable(ctx context.Context, cnt countFn, expected int64, interval, maxWait time.Duration) (int64, pollOutcome, error) {
	var last int64 = -1
	stable := 0
	start := time.Now()
	for {
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			if last >= 0 {
				return last, pollCtxDone, nil
			}
			return 0, pollCtxDone, ctx.Err()
		}
		rows, err := cnt(ctx)
		if err != nil {
			return 0, pollCtxDone, err
		}
		if rows >= expected {
			return rows, pollReachedExpected, nil
		}
		if rows == last {
			if stable++; stable >= noLossStableRounds {
				return rows, pollPlateau, nil
			}
		} else {
			stable, last = 0, rows
		}
		if time.Since(start) >= maxWait {
			return rows, pollMaxWait, nil
		}
	}
}
