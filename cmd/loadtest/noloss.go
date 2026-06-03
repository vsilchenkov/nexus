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

// noLossResult — результат сверки «нет потерь» (§10.2) с ClickHouse-логом.
type noLossResult struct {
	enabled  bool
	rows     int64 // строк type IN (requestAsync, RabbitMQAsync) в CH-логе
	expected int64 // async + rmq отправлено (at-least-once-пути)
	ok       bool
	err      error
}

// checkNoLoss сверяет число async/rmq-строк в CH-логе с expected. Колонка type
// хранит RootMethod (send.go), async-consumer пишет requestAsync и для
// RMQ-сообщений (async.go) — поэтому фильтруем оба значения, чтобы sync-строки
// не забивали счётчик. Ждёт flushGrace до запроса: sender пишет в CH батчами.
//
// Критерий: rows >= expected (потеря → rows < expected; at-least-once-дубликаты
// дают rows > expected и нарушением НЕ считаются). Пустой --ch-addr = пропуск.
func checkNoLoss(ctx context.Context, f flags, expected int64) noLossResult {
	if f.CHAddr == "" {
		return noLossResult{}
	}
	res := noLossResult{enabled: true, expected: expected}
	if !tableNameRe.MatchString(f.CHTable) {
		res.err = fmt.Errorf("invalid --ch-table %q", f.CHTable)
		return res
	}

	fmt.Printf("no-loss: ждём %s flush ClickHouse-буфера sender'а...\n", f.CHFlushGrace)
	select {
	case <-time.After(f.CHFlushGrace):
	case <-ctx.Done():
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

	qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// #nosec G201 -- f.CHTable провалидирован tableNameRe; фильтр — константы.
	query := fmt.Sprintf("SELECT count() FROM %s WHERE type IN ('requestAsync','RabbitMQAsync')", f.CHTable)
	var rows uint64
	if err := conn.QueryRow(qctx, query).Scan(&rows); err != nil {
		res.err = fmt.Errorf("count %s: %w", f.CHTable, err)
		return res
	}
	res.rows = int64(rows)
	res.ok = res.rows >= expected
	return res
}
