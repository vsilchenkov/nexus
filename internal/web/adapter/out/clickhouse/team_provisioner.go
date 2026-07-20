package clickhouse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"nexus/internal/domain"
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

	// Симметрично — цель: RENAME на занятое имя падает («Table already
	// exists»). Для переноса узла это тоже не ошибка: в целевой команде уже
	// есть таблица с этим именем, узел просто начнёт писать в неё.
	toDB, toTbl, _ := splitDBDotTable(to)
	var targetCnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name = ?",
		toDB, toTbl).Scan(&targetCnt); err != nil {
		return fmt.Errorf("check target table %s: %w", to, err)
	}
	if targetCnt > 0 {
		return port.ErrTargetTableExists
	}

	if err := conn.Exec(ctx, fmt.Sprintf("RENAME TABLE %s TO %s", from, to)); err != nil {
		return fmt.Errorf("rename table %s to %s: %w", from, to, err)
	}
	p.logger.Info("clickhouse table renamed",
		p.logger.Str("from", from), p.logger.Str("to", to))
	return nil
}

func (p *TeamProvisionerCH) CreateTable(ctx context.Context, table, ddl string) error {
	if !fullTableNamePattern.MatchString(table) {
		return errInvalidTableName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	if err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("create table %s: %w", table, err)
	}
	p.logger.Info("clickhouse table provisioned", p.logger.Str("table", table))
	return nil
}

func (p *TeamProvisionerCH) VerifyTemplate(ctx context.Context, db string, tmpl *domain.CHTemplate) error {
	if !dbNamePattern.MatchString(db) {
		return errInvalidDBName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	suffix, err := randHex(8)
	if err != nil {
		return err
	}
	tmpTable := fmt.Sprintf("%s.__nexus_tmpl_check_%s", db, suffix)
	ddl, err := tmpl.RenderCreateTable(tmpTable, 1)
	if err != nil {
		return err
	}
	// Чистим временную таблицу даже при отменённом ctx (WithoutCancel).
	defer func() {
		_ = conn.Exec(context.WithoutCancel(ctx), "DROP TABLE IF EXISTS "+tmpTable)
	}()
	if err := conn.Exec(ctx, ddl); err != nil {
		return fmt.Errorf("verify template: %w", err)
	}
	return nil
}

// randHex возвращает n hex-символов из crypto/rand.
func randHex(n int) (string, error) {
	b := make([]byte, n/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(b), nil
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
