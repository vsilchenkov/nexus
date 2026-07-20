package port

import (
	"context"
	"errors"

	"nexus/internal/domain"
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
	// таблица будет создана внешне при первом логе в новой БД). Если таблица с
	// таким именем уже есть в целевой БД — ErrTargetTableExists (RENAME на
	// занятое имя падает; узел просто начнёт писать в существующую таблицу).
	RenameTable(ctx context.Context, from, to string) error

	// CreateTable выполняет готовый `CREATE TABLE IF NOT EXISTS` DDL (§19).
	// ddl рендерится доменным CHTemplate.RenderCreateTable; здесь имя таблицы
	// (формат "<db>.<table>") валидируется и DDL выполняется. Идемпотентно.
	CreateTable(ctx context.Context, table, ddl string) error

	// VerifyTemplate «вживую» проверяет шаблон: пробно создаёт временную
	// таблицу в БД db (формат "nexus_<slug>") и сразу удаляет её. Ловит
	// несовместимость CODEC/типов для конкретной версии ClickHouse (§19.4).
	VerifyTemplate(ctx context.Context, db string, tmpl *domain.CHTemplate) error
}

// ErrSourceTableAbsent — исходной таблицы для RenameTable нет в ClickHouse.
// Не фатально для переноса узла (PG-метаданные авторитетны).
var ErrSourceTableAbsent = errors.New("clickhouse: source table absent")

// ErrTargetTableExists — в целевой БД уже есть таблица с этим именем.
// Не фатально для переноса узла: RENAME пропускается, узел пишет в неё.
var ErrTargetTableExists = errors.New("clickhouse: target table already exists")
