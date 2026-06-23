package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"

	"nexus/internal/domain"
)

// ensureCHTable создаёт лог-таблицу loadtest из КАНОННОЙ схемы — того же
// рендерера (domain.CHTemplate.RenderCreateTable из RequiredLogColumns), что
// генерирует прод-таблицы узлов. Это единый источник истины: при добавлении
// колонки в RequiredLogColumns (напр. http_method §39) таблица loadtest
// получает её автоматически. Раньше DDL дублировался рукописным CREATE TABLE в
// .gitlab-ci.yml и отставал от схемы — Sender падал на batch INSERT
// ("No such column …"), async/rmq логи не доходили до CH, no-loss проверка
// валила job. Пустой --ch-addr = пропуск (локальный `make loadtest` без CH).
func ensureCHTable(ctx context.Context, f flags) error {
	if f.CHAddr == "" {
		return nil
	}
	if !tableNameRe.MatchString(f.CHTable) {
		return fmt.Errorf("invalid --ch-table %q", f.CHTable)
	}
	// DefaultCHTemplateSpec рендерится ровно в схему §4.3 (MergeTree, без
	// CODEC/индексов/TTL) — то, что нужно тесту. RenderCreateTable требует
	// имя вида db.table и сам валидирует его (анти-инъекция).
	tmpl := domain.CHTemplate{Name: "loadtest", Spec: domain.DefaultCHTemplateSpec()}
	ddl, err := tmpl.RenderCreateTable(f.CHTable, 0)
	if err != nil {
		return fmt.Errorf("render create table %s: %w", f.CHTable, err)
	}

	conn, err := chgo.Open(&chgo.Options{
		Addr:        []string{f.CHAddr},
		Auth:        chgo.Auth{Username: f.CHUser, Password: f.CHPassword},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("clickhouse open %s: %w", f.CHAddr, err)
	}
	defer conn.Close()

	ectx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if db := databaseOf(f.CHTable); db != "" {
		// db — часть провалидированного tableNameRe имени ([A-Za-z_][A-Za-z0-9_]*),
		// безопасна для прямой подстановки.
		if err := conn.Exec(ectx, "CREATE DATABASE IF NOT EXISTS "+db); err != nil {
			return fmt.Errorf("create database %s: %w", db, err)
		}
	}
	if err := conn.Exec(ectx, ddl); err != nil {
		return fmt.Errorf("create table %s: %w", f.CHTable, err)
	}
	return nil
}

// databaseOf возвращает префикс БД из "db.table"; пустую строку, если имя без точки.
func databaseOf(table string) string {
	if i := strings.IndexByte(table, '.'); i > 0 {
		return table[:i]
	}
	return ""
}
