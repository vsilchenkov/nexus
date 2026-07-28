package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

// §70.3: маркер владения ClickHouse-БД.
//
// Несколько нод Nexus (у каждой свои PostgreSQL/Redis/Kafka) могут писать в один
// ClickHouse. Имена БД разведены суффиксом ноды (§70.2), но одного этого мало:
// слаг команды допускает '_', поэтому нода без идентификатора с командой
// "kz_edo" и нода "kz" с командой "edo" дают одно имя. Владение поэтому
// определяется не разбором имени, а служебной таблицей в самой БД.

// MarkerTable — имя служебной таблицы-маркера внутри каждой БД ноды.
const MarkerTable = "__nexus_owner"

// markerSchemaVersion — версия схемы маркера; на будущее, если понадобится
// добавить поля и отличать старые записи.
const markerSchemaVersion = 1

// chCodeTableAlreadyExists — SQLSTATE-аналог ClickHouse (57). Возникает у
// проигравшего гонку `CREATE TABLE` без IF NOT EXISTS — именно на этом коде
// построен атомарный захват (см. Claim).
const chCodeTableAlreadyExists = 57

// Ошибки владения. Разрушающие операции по чужой БД не выполняются никогда —
// ни с какими аварийными флагами.
var (
	// ErrForeignDatabase — БД помечена маркером другой ноды.
	ErrForeignDatabase = domain.ErrCHForeignDatabase
	// ErrOwnershipConflict — в маркере несколько разных идентификаторов
	// (ручной INSERT либо восстановление бэкапа ClickHouse). Чинится вручную:
	// DROP TABLE <db>.__nexus_owner и перезапуск нужной ноды.
	ErrOwnershipConflict = errors.New("clickhouse: ownership marker holds several instance ids")
	// ErrFirstRunDatabaseExists — гейт первого запуска (§70.5): нода поднимается
	// со свежей PostgreSQL, а её БД в ClickHouse уже существует.
	ErrFirstRunDatabaseExists = errors.New("clickhouse: database already exists on first run of this instance")
)

// dbNameRe — имя БД идёт в DDL напрямую (CH не принимает параметризованные
// имена), поэтому проверяется жёстко.
var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Verdict — результат проверки владения БД.
type Verdict uint8

const (
	// VerdictUnknown — определить не удалось (ClickHouse недоступен, запрос упал
	// либо маркер существует, но пуст — сосед умер между CREATE и INSERT).
	VerdictUnknown Verdict = iota
	// VerdictOwned — маркер несёт наш идентификатор.
	VerdictOwned
	// VerdictForeign — маркер несёт чужой идентификатор.
	VerdictForeign
	// VerdictUnclaimed — БД есть, маркера нет (нода до §70 либо БД создана
	// вручную).
	VerdictUnclaimed
	// VerdictNoDatabase — БД не существует.
	VerdictNoDatabase
	// VerdictConflict — в маркере несколько разных идентификаторов.
	VerdictConflict
)

func (v Verdict) String() string {
	switch v {
	case VerdictOwned:
		return "owned"
	case VerdictForeign:
		return "foreign"
	case VerdictUnclaimed:
		return "unclaimed"
	case VerdictNoDatabase:
		return "no_database"
	case VerdictConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// Owner — содержимое маркера (диагностическое).
type Owner struct {
	InstanceID string
	ClaimedBy  string
	ClaimedAt  time.Time
}

// Значения по умолчанию для кеша вердиктов.
//
// Проверка владения вызывается на каждый запрос логов и метрик, поэтому без
// кеша каждый показ страницы стоил бы двух запросов к system.*. Отрицательные
// вердикты живут меньше: исправление конфигурации должно подхватываться быстро.
const (
	defaultOwnerPositiveTTL = 5 * time.Minute
	defaultOwnerNegativeTTL = 30 * time.Second
	defaultOwnerQueryTO     = 5 * time.Second
	// ownerClaimRetries/Delay — окно между CREATE TABLE и INSERT у соседа,
	// выигравшего гонку захвата: его маркер уже существует, но ещё пуст.
	ownerClaimRetries = 5
	ownerClaimDelay   = 200 * time.Millisecond
)

// GuardOption — функциональная опция конструктора NewGuard.
type GuardOption func(*Guard)

// WithOwnerTTL задаёт время жизни положительного и отрицательного вердикта.
func WithOwnerTTL(positive, negative time.Duration) GuardOption {
	return func(g *Guard) { g.positiveTTL, g.negativeTTL = positive, negative }
}

// WithOwnerQueryTimeout задаёт потолок времени на запросы владения.
func WithOwnerQueryTimeout(d time.Duration) GuardOption {
	return func(g *Guard) { g.queryTimeout = d }
}

// WithOwnerClock подменяет источник времени (тесты кеша).
func WithOwnerClock(now func() time.Time) GuardOption {
	return func(g *Guard) { g.now = now }
}

// Guard — владелец политики «наша БД / чужая БД» для одной ноды.
//
// Потокобезопасен. Держится в App каждого сервиса и передаётся адаптерам и
// usecase'ам как узкий интерфейс на их стороне (CLAUDE.md §3).
type Guard struct {
	conn         ConnProvider
	instanceID   domain.InstanceID
	logger       logging.Logger
	positiveTTL  time.Duration
	negativeTTL  time.Duration
	queryTimeout time.Duration
	now          func() time.Time

	mu    sync.Mutex
	cache map[string]cachedVerdict
}

type cachedVerdict struct {
	verdict Verdict
	owner   *Owner
	at      time.Time
	// err — сбой проверки. Кешируется вместе с вердиктом, чтобы лежащий
	// ClickHouse не получал запрос на каждое обращение к логам, и при этом
	// вызывающие продолжали видеть НАСТОЯЩУЮ причину: без этого поля повторные
	// вызовы получали бы «вердикт unknown» без ошибки, и уборка схемы
	// рапортовала бы «таблица принадлежит другой ноде» вместо «ClickHouse не
	// ответил».
	err error
}

// NewGuard создаёт гейт владения для ноды instanceID.
func NewGuard(conn ConnProvider, instanceID domain.InstanceID, logger logging.Logger, opts ...GuardOption) *Guard {
	g := &Guard{
		conn:         conn,
		instanceID:   instanceID,
		logger:       logger,
		positiveTTL:  defaultOwnerPositiveTTL,
		negativeTTL:  defaultOwnerNegativeTTL,
		queryTimeout: defaultOwnerQueryTO,
		now:          time.Now,
		cache:        make(map[string]cachedVerdict, 8),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// InstanceID — идентификатор ноды, от имени которой работает гейт.
func (g *Guard) InstanceID() domain.InstanceID { return g.instanceID }

// Invalidate сбрасывает кеш вердиктов. Вызывается после hot-reload
// ClickHouse-соединения (§8.4): на новом сервере наших маркеров может не быть,
// и старые вердикты относились бы к прежнему серверу.
func (g *Guard) Invalidate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	clear(g.cache)
}

// Check возвращает вердикт по БД. Результат кешируется (положительный —
// positiveTTL, остальные — negativeTTL).
func (g *Guard) Check(ctx context.Context, db string) (Verdict, *Owner, error) {
	if !dbNameRe.MatchString(db) {
		return VerdictUnknown, nil, fmt.Errorf("clickhouse: invalid database name %q", db)
	}
	if e, ok := g.cached(db); ok {
		return e.verdict, e.owner, e.err
	}

	v, o, err := g.probe(ctx, db)
	if err != nil {
		// Сбой кешируется вместе с ошибкой на negativeTTL: лежащий ClickHouse не
		// должен получать запрос на каждое обращение к логам, но и «молчаливый
		// unknown» отдавать нельзя — вызывающие обязаны видеть причину.
		g.storeErr(db, err)
		return VerdictUnknown, nil, err
	}
	g.store(db, v, o)
	g.logger.Debug("clickhouse ownership resolved",
		g.logger.Str("database", db),
		g.logger.Str("verdict", v.String()),
		g.logger.Str("instance", g.instanceID.String()))
	return v, o, nil
}

// OwnsDatabase — строгая проверка: БД принадлежит этой ноде (маркер наш).
// Всё остальное (чужая, без маркера, неизвестно) — false. Используется перед
// разрушающими операциями, где неопределённость обязана блокировать.
func (g *Guard) OwnsDatabase(ctx context.Context, db string) (bool, error) {
	v, _, err := g.Check(ctx, db)
	if err != nil {
		return false, err
	}
	return v == VerdictOwned, nil
}

// OwnsTable — то же для полного имени "db.table".
func (g *Guard) OwnsTable(ctx context.Context, table string) (bool, error) {
	db, ok := databaseOf(table)
	if !ok {
		return false, fmt.Errorf("clickhouse: invalid table name %q", table)
	}
	return g.OwnsDatabase(ctx, db)
}

// IsForeignDatabase — БД ЯВНО принадлежит другой ноде (чужой маркер либо
// конфликт владения). Отличается от !OwnsDatabase тем, что «маркера нет» и
// «неизвестно» не считаются чужим.
//
// Нужен там, где отказ адресован оператору, а не защищает данные: сохранение
// узла с таблицей в чужой БД (§70.6). Строгая проверка запрещала бы сохранение
// узла на ноде, где маркеры ещё не проставлены (обновление до §70) или где
// ClickHouse временно недоступен, — то есть ломала бы работу из-за состояния
// стороннего сервиса.
func (g *Guard) IsForeignDatabase(ctx context.Context, db string) (bool, error) {
	v, _, err := g.Check(ctx, db)
	if err != nil {
		return false, err
	}
	return v == VerdictForeign || v == VerdictConflict, nil
}

// IsForeignTable — то же по полному имени "db.table".
func (g *Guard) IsForeignTable(ctx context.Context, table string) (bool, error) {
	db, ok := databaseOf(table)
	if !ok {
		return false, fmt.Errorf("clickhouse: invalid table name %q", table)
	}
	return g.IsForeignDatabase(ctx, db)
}

// MayManageTable — мягкая проверка для идемпотентных операций обслуживания
// схемы (стартовые ALTER … ADD COLUMN IF NOT EXISTS и backfill): разрешает
// работу и с БД без маркера.
//
// Почему мягко: список таблиц берётся из СВОЕЙ PostgreSQL, а маркеры на боевой
// ноде появляются только после первого старта Web с §70. Строгая проверка
// означала бы, что при обновлении Sender раньше Web стартовые ALTER молча не
// применяются, и вставка по новой схеме падает. Чужой маркер (VerdictForeign) и
// конфликт по-прежнему запрещают операцию.
func (g *Guard) MayManageTable(ctx context.Context, table string) (bool, error) {
	db, ok := databaseOf(table)
	if !ok {
		return false, fmt.Errorf("clickhouse: invalid table name %q", table)
	}
	v, _, err := g.Check(ctx, db)
	if err != nil {
		return false, err
	}
	switch v {
	case VerdictOwned, VerdictUnclaimed, VerdictNoDatabase:
		return true, nil
	default:
		return false, nil
	}
}

// FilterManagedTables оставляет только те таблицы, схемой которых этой ноде
// разрешено управлять (см. MayManageTable). Отфильтрованные логируются WARN —
// пропуск обслуживания схемы должен быть виден.
func (g *Guard) FilterManagedTables(ctx context.Context, tables []string) []string {
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		ok, err := g.MayManageTable(ctx, t)
		if err != nil {
			g.logger.Warn("clickhouse ownership check failed; skipping table maintenance",
				g.logger.Str("table", t), g.logger.Err(err))
			continue
		}
		if !ok {
			g.logger.Warn("table belongs to another nexus instance; schema maintenance skipped",
				g.logger.Str("table", t))
			continue
		}
		out = append(out, t)
	}
	return out
}

// AssertOwnsDatabase — гейт перед разрушающей операцией. Возвращает
// ErrForeignDatabase для чужой БД и ошибку для любого неопределённого случая.
func (g *Guard) AssertOwnsDatabase(ctx context.Context, db string) error {
	v, o, err := g.Check(ctx, db)
	if err != nil {
		return err
	}
	switch v {
	case VerdictOwned:
		return nil
	case VerdictForeign:
		return fmt.Errorf("%w: %s (owner=%q)", ErrForeignDatabase, db, ownerID(o))
	case VerdictConflict:
		return fmt.Errorf("%w: %s", ErrOwnershipConflict, db)
	default:
		return fmt.Errorf("%w: %s (verdict=%s)", ErrForeignDatabase, db, v)
	}
}

// AssertOwnsTable — то же для полного имени "db.table".
func (g *Guard) AssertOwnsTable(ctx context.Context, table string) error {
	db, ok := databaseOf(table)
	if !ok {
		return fmt.Errorf("clickhouse: invalid table name %q", table)
	}
	return g.AssertOwnsDatabase(ctx, db)
}

// Claim захватывает БД за этой нодой: создаёт её при необходимости и ставит
// маркер. Идемпотентен — повторный вызов на своей БД ничего не делает.
//
// Атомарность обеспечивает `CREATE TABLE` БЕЗ `IF NOT EXISTS`: ClickHouse
// сериализует создание одноимённой таблицы, проигравший получает код 57 и
// читает уже записанный маркер. Внешних блокировок не требуется.
func (g *Guard) Claim(ctx context.Context, db string) error {
	if !dbNameRe.MatchString(db) {
		return fmt.Errorf("clickhouse: invalid database name %q", db)
	}
	conn := g.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	defer g.forget(db)

	if err := conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+db); err != nil {
		return fmt.Errorf("create database %s: %w", db, err)
	}

	err := conn.Exec(ctx, fmt.Sprintf(`
CREATE TABLE %s.%s (
	instance_id String,
	claimed_at  DateTime,
	claimed_by  String,
	schema_ver  UInt8
) ENGINE = TinyLog`, db, MarkerTable))
	switch {
	case err == nil:
		// Мы создали маркер — записываем себя.
		if err := g.writeMarker(ctx, conn, db); err != nil {
			return err
		}
		g.logger.Info("clickhouse database claimed",
			g.logger.Str("database", db),
			g.logger.Str("instance", g.instanceID.String()))
		return nil
	case !isTableAlreadyExists(err):
		return fmt.Errorf("create ownership marker in %s: %w", db, err)
	}

	// Маркер уже есть — читаем, чей он.
	for attempt := range ownerClaimRetries {
		owners, err := g.readMarker(ctx, conn, db)
		if err != nil {
			return err
		}
		switch verdict := g.verdictFor(owners); verdict {
		case VerdictOwned:
			return nil
		case VerdictForeign:
			return fmt.Errorf("%w: %s (owner=%q)", ErrForeignDatabase, db, owners[0].InstanceID)
		case VerdictConflict:
			return fmt.Errorf("%w: %s", ErrOwnershipConflict, db)
		}
		// Пустой маркер: сосед выиграл CREATE, но ещё не вставил строку — ждём.
		if attempt < ownerClaimRetries-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(ownerClaimDelay):
			}
		}
	}

	// Маркер так и остался пустым: сосед умер между CREATE и INSERT. Лечим,
	// записывая себя — иначе БД навсегда осталась бы «ничьей».
	g.logger.Warn("empty ownership marker found; claiming it",
		g.logger.Str("database", db),
		g.logger.Str("instance", g.instanceID.String()))
	return g.writeMarker(ctx, conn, db)
}

// EnsureOptions — параметры стартового гейта EnsureAll.
type EnsureOptions struct {
	// NeverClaimed — эта нода ещё ни разу не захватывала свои БД
	// (instance_identity.ch_claimed = false).
	NeverClaimed bool
	// FreshPG — в PostgreSQL ноды нет узлов и нет команд, кроме сидированной.
	//
	// Гейт первого запуска требует ОБОИХ признаков: «нода ничего не захватывала»
	// и «нода ещё ничего не накопила». Каждого по отдельности мало —
	// оба варианта проверены на стенде:
	//   - только FreshPG: у только что развёрнутой ноды узлов нет и на втором
	//     старте, и гейт срабатывал бы на её собственную БД;
	//   - только NeverClaimed: при обновлении действующей ноды до §70 флага
	//     захвата ещё нет, и она отказывалась подниматься на своих же данных.
	FreshPG bool
	// Adopt — аварийный обход гейта первого запуска (--ch-adopt /
	// instance.adopt_unowned): PostgreSQL пересоздали, а ClickHouse остался.
	// Чужой маркер он не перебивает.
	Adopt bool
}

// EnsureAll — стартовый гейт владения (§70.5). Проверяет и при необходимости
// захватывает все БД этой ноды. Любая чужая БД, конфликт или срабатывание гейта
// первого запуска — ошибка, по которой сервис не должен подниматься.
func (g *Guard) EnsureAll(ctx context.Context, dbs []string, opts EnsureOptions) error {
	conn := g.conn.Conn()
	if conn == nil {
		return errors.New("clickhouse conn is nil")
	}
	seen := make(map[string]bool, len(dbs))
	for _, db := range dbs {
		if db == "" || seen[db] {
			continue
		}
		seen[db] = true
		if err := g.ensureOne(ctx, conn, db, opts); err != nil {
			return err
		}
	}
	return nil
}

func (g *Guard) ensureOne(ctx context.Context, conn driver.Conn, db string, opts EnsureOptions) error {
	if !dbNameRe.MatchString(db) {
		return fmt.Errorf("clickhouse: invalid database name %q", db)
	}
	exists, err := g.databaseExists(ctx, conn, db)
	if err != nil {
		return err
	}

	if !exists {
		return g.Claim(ctx, db)
	}

	v, o, err := g.Check(ctx, db)
	if err != nil {
		return err
	}

	// Гейт первого запуска (§70.5): БД существует, а нода только что развёрнута
	// (ничего не захватывала и ничего не накопила) — значит, в эту БД пишет
	// кто-то другой.
	//
	// Свой маркер снимает подозрение ТОЛЬКО у ноды с непустым instance.id: там
	// идентификатор уникален по договорённости, и «маркер наш» означает «БД
	// наша». У ноды без идентификатора маркер соседа выглядит своим
	// (instance_id='' у обеих), поэтому там гейт срабатывает даже на Owned.
	provenOurs := v == VerdictOwned && !g.instanceID.IsZero()
	if opts.NeverClaimed && opts.FreshPG && !opts.Adopt && !provenOurs {
		return fmt.Errorf("%w: %s (verdict=%s); set a unique instance.id in the config of this node "+
			"(or start with --ch-adopt if this database really belongs to it)",
			ErrFirstRunDatabaseExists, db, v)
	}

	switch v {
	case VerdictOwned:
		return nil
	case VerdictUnclaimed:
		// Нода до §70 либо БД, созданная вручную: помечаем своей. Её имя пришло
		// из наших же teams.ch_database, других претендентов нет.
		g.logger.Info("adopting unmarked clickhouse database",
			g.logger.Str("database", db),
			g.logger.Str("instance", g.instanceID.String()))
		return g.Claim(ctx, db)
	case VerdictForeign:
		return fmt.Errorf("%w: %s (owner=%q)", ErrForeignDatabase, db, ownerID(o))
	case VerdictConflict:
		return fmt.Errorf("%w: %s", ErrOwnershipConflict, db)
	default:
		return fmt.Errorf("clickhouse: cannot determine ownership of %s", db)
	}
}

// probe выполняет фактические запросы владения (без кеша).
func (g *Guard) probe(ctx context.Context, db string) (Verdict, *Owner, error) {
	conn := g.conn.Conn()
	if conn == nil {
		return VerdictUnknown, nil, errors.New("clickhouse conn is nil")
	}
	qctx, cancel := context.WithTimeout(ctx, g.queryTimeout)
	defer cancel()

	exists, err := g.databaseExists(qctx, conn, db)
	if err != nil {
		return VerdictUnknown, nil, err
	}
	if !exists {
		return VerdictNoDatabase, nil, nil
	}

	marked, err := g.markerExists(qctx, conn, db)
	if err != nil {
		return VerdictUnknown, nil, err
	}
	if !marked {
		return VerdictUnclaimed, nil, nil
	}

	owners, err := g.readMarker(qctx, conn, db)
	if err != nil {
		return VerdictUnknown, nil, err
	}
	v := g.verdictFor(owners)
	if len(owners) == 0 {
		return v, nil, nil
	}
	return v, &owners[0], nil
}

// verdictFor превращает содержимое маркера в вердикт. Пустой маркер — это
// именно неопределённость (сосед мог не успеть вставить строку), а не «ничей».
func (g *Guard) verdictFor(owners []Owner) Verdict {
	if len(owners) == 0 {
		return VerdictUnknown
	}
	ids := make(map[string]struct{}, len(owners))
	for _, o := range owners {
		ids[o.InstanceID] = struct{}{}
	}
	if len(ids) > 1 {
		return VerdictConflict
	}
	if owners[0].InstanceID == g.instanceID.String() {
		return VerdictOwned
	}
	return VerdictForeign
}

func (g *Guard) databaseExists(ctx context.Context, conn driver.Conn, db string) (bool, error) {
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.databases WHERE name = ?", db).Scan(&cnt); err != nil {
		return false, fmt.Errorf("check database %s: %w", db, err)
	}
	return cnt > 0, nil
}

func (g *Guard) markerExists(ctx context.Context, conn driver.Conn, db string) (bool, error) {
	var cnt uint64
	if err := conn.QueryRow(ctx,
		"SELECT count() FROM system.tables WHERE database = ? AND name = ?", db, MarkerTable).Scan(&cnt); err != nil {
		return false, fmt.Errorf("check ownership marker in %s: %w", db, err)
	}
	return cnt > 0, nil
}

// readMarker читает строки маркера, отсортированные по времени заявки:
// owners[0] — тот, кто заявил первым (детерминированный «победитель» при
// разборе конфликта).
func (g *Guard) readMarker(ctx context.Context, conn driver.Conn, db string) ([]Owner, error) {
	rows, err := conn.Query(ctx, fmt.Sprintf(
		"SELECT instance_id, claimed_by, claimed_at FROM %s.%s LIMIT 100", db, MarkerTable))
	if err != nil {
		return nil, fmt.Errorf("read ownership marker in %s: %w", db, err)
	}
	defer rows.Close()

	var out []Owner
	for rows.Next() {
		var o Owner
		if err := rows.Scan(&o.InstanceID, &o.ClaimedBy, &o.ClaimedAt); err != nil {
			return nil, fmt.Errorf("scan ownership marker in %s: %w", db, err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ownership marker in %s: %w", db, err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ClaimedAt.Equal(out[j].ClaimedAt) {
			return out[i].InstanceID < out[j].InstanceID
		}
		return out[i].ClaimedAt.Before(out[j].ClaimedAt)
	})
	return out, nil
}

func (g *Guard) writeMarker(ctx context.Context, conn driver.Conn, db string) error {
	if err := conn.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s.%s (instance_id, claimed_at, claimed_by, schema_ver) VALUES (?, now(), ?, ?)",
		db, MarkerTable), g.instanceID.String(), claimedBySignature(), markerSchemaVersion); err != nil {
		return fmt.Errorf("write ownership marker in %s: %w", db, err)
	}
	g.forget(db)
	return nil
}

// cached возвращает закешированную запись (вердикт + ошибку проверки).
// ok=false — кеша нет либо он протух.
func (g *Guard) cached(db string) (cachedVerdict, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.cache[db]
	if !ok {
		return cachedVerdict{}, false
	}
	ttl := g.negativeTTL
	if e.verdict == VerdictOwned {
		ttl = g.positiveTTL
	}
	if g.now().Sub(e.at) >= ttl {
		return cachedVerdict{}, false
	}
	return e, true
}

func (g *Guard) store(db string, v Verdict, o *Owner) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache[db] = cachedVerdict{verdict: v, owner: o, at: g.now()}
}

// storeErr запоминает сбой проверки на negativeTTL — см. cachedVerdict.err.
func (g *Guard) storeErr(db string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cache[db] = cachedVerdict{verdict: VerdictUnknown, at: g.now(), err: err}
}

func (g *Guard) forget(db string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cache, db)
}

// claimedBySignature — диагностическая подпись «кто заявил» (хост процесса).
// На решения не влияет, поэтому ошибка os.Hostname гасится осознанно.
func claimedBySignature() string {
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}

// databaseOf режет полное имя "db.table". Требует ровно одну точку и
// допустимые символы с обеих сторон — как isSafeTableName в CH-адаптерах:
// имя таблицы попадает в DDL напрямую, и «почти валидное» здесь опаснее отказа.
func databaseOf(table string) (string, bool) {
	db, tbl, found := strings.Cut(table, ".")
	if !found || !dbNameRe.MatchString(db) || !dbNameRe.MatchString(tbl) {
		return "", false
	}
	return db, true
}

func ownerID(o *Owner) string {
	if o == nil {
		return ""
	}
	return o.InstanceID
}

// isTableAlreadyExists — проигрыш гонки CREATE TABLE (код 57). Именно на этом
// основан атомарный захват, поэтому код проверяется явно, а не по тексту.
func isTableAlreadyExists(err error) bool {
	var ex *chgo.Exception
	if !errors.As(err, &ex) {
		return false
	}
	return ex.Code == chCodeTableAlreadyExists
}
