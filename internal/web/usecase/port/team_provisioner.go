package port

import "context"

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
}
