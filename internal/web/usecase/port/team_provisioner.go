package port

import (
	"context"
	"errors"
)

// TeamProvisioner — физический provisioning ClickHouse-объектов команды
// (multi-tenancy v2). Реализуется adapter'ом поверх chdriver.Conn.
//
// Отделён от TeamRepo, чтобы TeamUsecase мог собрать атомарную
// PG-tx + CH-команду (rollback PG при упавшем CH).
type TeamProvisioner interface {
	// CreateDatabase создаёт `CREATE DATABASE IF NOT EXISTS <name>` в
	// ClickHouse. Идемпотентно — повторный вызов с тем же именем не
	// возвращает ошибку.
	CreateDatabase(ctx context.Context, name string) error

	// DropDatabase удаляет БД `DROP DATABASE IF EXISTS <name>`. Используется
	// только из UI «Orphans» после явного подтверждения админа — TeamUsecase
	// сам этот метод не зовёт (см. §16/Phase 10.D).
	DropDatabase(ctx context.Context, name string) error

	// RenameTable переносит таблицу между БД на том же ClickHouse-сервере
	// (`RENAME TABLE from TO to`). Используется при переносе узла между
	// командами (Phase 11.B): логи следуют за узлом. Оба имени в формате
	// "<db>.<table>". Если исходной таблицы нет — возвращает
	// ErrSourceTableAbsent (узел переносится, но CH-операция пропускается:
	// таблица будет создана внешне при первом логе в новой БД).
	RenameTable(ctx context.Context, from, to string) error
}

// ErrSourceTableAbsent — исходной таблицы для RenameTable нет в ClickHouse.
// Не фатально для переноса узла (PG-метаданные авторитетны).
var ErrSourceTableAbsent = errors.New("clickhouse: source table absent")
