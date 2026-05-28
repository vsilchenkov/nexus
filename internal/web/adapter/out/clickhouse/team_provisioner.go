package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// TeamProvisionerCH — реализация port.TeamProvisioner поверх chdriver.Conn
// через ConnProvider (Phase 10.C.1). CREATE/DROP DATABASE выполняются
// напрямую в ClickHouse — параметризованного синтаксиса для имени БД
// в CH нет, поэтому имя проходит через жёсткий regex (защита от
// SQL-инъекции).
type TeamProvisionerCH struct {
	conn   ConnProvider
	logger logging.Logger
}

var _ port.TeamProvisioner = (*TeamProvisionerCH)(nil)

func NewTeamProvisioner(conn ConnProvider, logger logging.Logger) *TeamProvisionerCH {
	return &TeamProvisionerCH{conn: conn, logger: logger}
}

// dbNamePattern совпадает с teamCHDatabasePattern в domain/team.go:
// "nexus_<slug>", где slug — [a-z][a-z0-9_]{0,31}.
var dbNamePattern = regexp.MustCompile(`^nexus_[a-z][a-z0-9_]{0,31}$`)

var errInvalidDBName = errors.New("clickhouse: invalid database name (expected nexus_<slug>)")

func (p *TeamProvisionerCH) CreateDatabase(ctx context.Context, name string) error {
	if !dbNamePattern.MatchString(name) {
		return errInvalidDBName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	if err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", name)); err != nil {
		return fmt.Errorf("create database %s: %w", name, err)
	}
	p.logger.Info("clickhouse database provisioned",
		p.logger.Str("database", name))
	return nil
}

func (p *TeamProvisionerCH) DropDatabase(ctx context.Context, name string) error {
	if !dbNamePattern.MatchString(name) {
		return errInvalidDBName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	if err := conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", name)); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	p.logger.Info("clickhouse database dropped",
		p.logger.Str("database", name))
	return nil
}

// fullTableNamePattern — "<db>.<table>", обе части [A-Za-z0-9_], ровно
// одна точка. CH не принимает параметризованные имена БД/таблиц, поэтому
// строки идут в Exec напрямую после жёсткой валидации (защита от инъекции).
var fullTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

var errInvalidTableName = errors.New("clickhouse: invalid table name (expected db.table)")

func (p *TeamProvisionerCH) RenameTable(ctx context.Context, from, to string) error {
	if !fullTableNamePattern.MatchString(from) || !fullTableNamePattern.MatchString(to) {
		return errInvalidTableName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}

	// Проверяем существование источника — RENAME несуществующей таблицы
	// падает, но для переноса узла это не ошибка (таблица могла ещё не
	// создаться, если в узел не приходили логи).
	fromDB, fromTbl, _ := splitDBDotTable(from)
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name = ?",
		fromDB, fromTbl).Scan(&cnt); err != nil {
		return fmt.Errorf("check source table %s: %w", from, err)
	}
	if cnt == 0 {
		return port.ErrSourceTableAbsent
	}

	if err := conn.Exec(ctx, fmt.Sprintf("RENAME TABLE %s TO %s", from, to)); err != nil {
		return fmt.Errorf("rename table %s to %s: %w", from, to, err)
	}
	p.logger.Info("clickhouse table renamed",
		p.logger.Str("from", from), p.logger.Str("to", to))
	return nil
}

// splitDBDotTable режет "db.table" (предполагается уже валидным).
func splitDBDotTable(name string) (db, table string, ok bool) {
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			return name[:i], name[i+1:], true
		}
	}
	return "", "", false
}
