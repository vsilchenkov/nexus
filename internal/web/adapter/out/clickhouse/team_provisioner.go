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

	// ownership — гейт владения БД (§70.4). nil = проверок нет (поведение до
	// §70): так собираются тесты и legacy-wiring без ClickHouse-Guard'а.
	ownership Ownership
}

// Ownership — узкий интерфейс гейта владения на стороне консьюмера
// (CLAUDE.md §3). Реализуется clickhouse.Guard структурным совпадением.
type Ownership interface {
	// Claim захватывает БД за этой нодой (создаёт при необходимости и ставит
	// маркер). Возвращает ошибку, если БД принадлежит другой ноде.
	Claim(ctx context.Context, db string) error
	// AssertOwnsDatabase — отказ, если БД не принадлежит этой ноде.
	AssertOwnsDatabase(ctx context.Context, db string) error
	// AssertOwnsTable — то же для полного имени "db.table".
	AssertOwnsTable(ctx context.Context, table string) error
}

var _ port.TeamProvisioner = (*TeamProvisionerCH)(nil)

func NewTeamProvisioner(conn ConnProvider, logger logging.Logger) *TeamProvisionerCH {
	return &TeamProvisionerCH{conn: conn, logger: logger}
}

// SetOwnership подключает гейт владения (§70.4). Вызывается в wiring Web после
// создания Guard'а; до вызова провижинер работает как до §70.
func (p *TeamProvisionerCH) SetOwnership(o Ownership) { p.ownership = o }

// assertOwns — гейт перед операцией, изменяющей объекты БД. Без подключённого
// Ownership пропускает всё (совместимость).
func (p *TeamProvisionerCH) assertOwns(ctx context.Context, db string) error {
	if p.ownership == nil {
		return nil
	}
	return p.ownership.AssertOwnsDatabase(ctx, db)
}

// assertOwnsTable — то же по полному имени таблицы.
func (p *TeamProvisionerCH) assertOwnsTable(ctx context.Context, table string) error {
	if p.ownership == nil {
		return nil
	}
	return p.ownership.AssertOwnsTable(ctx, table)
}

// dbNamePattern совпадает с teamCHDatabasePattern в domain/team.go:
// "nexus_" + [<instance_id>_] + slug (§70.2 расширил хвост до 40 символов).
var dbNamePattern = regexp.MustCompile(`^nexus_[a-z][a-z0-9_]{0,40}$`)

var errInvalidDBName = errors.New("clickhouse: invalid database name (expected nexus_<slug>)")

// CreateDatabase создаёт БД команды. При подключённом гейте владения (§70.4)
// вместо голого CREATE выполняется захват: имя БД может совпасть с чужим даже
// при разных идентификаторах нод (слаг допускает '_', см. §70.2), и тогда
// создание команды обязано провалиться, а не молча подключить нас к чужим
// данным. Ошибка захвата откатывает PG-запись в TeamUsecase.Create.
func (p *TeamProvisionerCH) CreateDatabase(ctx context.Context, name string) error {
	if !dbNamePattern.MatchString(name) {
		return errInvalidDBName
	}
	if p.ownership != nil {
		if err := p.ownership.Claim(ctx, name); err != nil {
			return fmt.Errorf("claim database %s: %w", name, err)
		}
		p.logger.Info("clickhouse database provisioned",
			p.logger.Str("database", name))
		return nil
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
	// §70.4: продакшн-вызовов у метода нет, но гейт ставится и здесь — удаление
	// чужой БД было бы самой разрушительной из возможных ошибок.
	if err := p.assertOwns(ctx, name); err != nil {
		return err
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

// TableExists — есть ли таблица "<db>.<table>" в ClickHouse (system.tables).
func (p *TeamProvisionerCH) TableExists(ctx context.Context, table string) (bool, error) {
	if !fullTableNamePattern.MatchString(table) {
		return false, errInvalidTableName
	}
	conn := p.conn.Conn()
	if conn == nil {
		return false, errors.New("clickhouse conn is nil")
	}
	db, name, _ := splitDBDotTable(table)
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name = ?",
		db, name).Scan(&cnt); err != nil {
		return false, fmt.Errorf("check table %s: %w", table, err)
	}
	return cnt > 0, nil
}

func (p *TeamProvisionerCH) RenameTable(ctx context.Context, from, to string) error {
	if !fullTableNamePattern.MatchString(from) || !fullTableNamePattern.MatchString(to) {
		return errInvalidTableName
	}
	// §70.4: проверяются ОБЕ стороны — перенос узла не должен ни забрать чужую
	// таблицу, ни положить свою в чужую БД.
	if err := p.assertOwnsTable(ctx, from); err != nil {
		return err
	}
	if err := p.assertOwnsTable(ctx, to); err != nil {
		return err
	}
	conn := p.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}

	// Проверяем существование источника — RENAME несуществующей таблицы
	// падает, но для переноса узла это не ошибка (таблица могла ещё не
	// создаться, если в узел не приходили логи).
	srcExists, err := p.TableExists(ctx, from)
	if err != nil {
		return err
	}
	if !srcExists {
		return port.ErrSourceTableAbsent
	}

	// Симметрично — цель: RENAME на занятое имя падает («Table already
	// exists»). Для переноса узла это тоже не ошибка: в целевой команде уже
	// есть таблица с этим именем, узел просто начнёт писать в неё.
	dstExists, err := p.TableExists(ctx, to)
	if err != nil {
		return err
	}
	if dstExists {
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
	// §70.4: создать таблицу в чужой БД нельзя — узел молча сел бы на чужую
	// схему, а расхождение всплыло бы позже на вставке в Sender.
	if err := p.assertOwnsTable(ctx, table); err != nil {
		return err
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
	// §70.4: проверка шаблона создаёт и удаляет временную таблицу — в чужой БД
	// этого делать нельзя (плюс её остатки засоряли бы чужой список таблиц).
	if err := p.assertOwns(ctx, db); err != nil {
		return err
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
