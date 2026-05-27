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
