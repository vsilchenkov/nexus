package port

import (
	"context"
	"errors"

	"nexus/internal/domain"
	"nexus/internal/domain/backup"
)

// ErrTargetNotClean — восстановление конфигурации запрещено: в экземпляре уже
// есть узлы или команды сверх сидированной (§110.5). Проверяется и при
// предпросмотре, и под блокировкой в транзакции записи.
var ErrTargetNotClean = errors.New("backup: target instance is not clean")

// ErrRestoreInProgress — другой импорт уже выполняется (§110.5).
var ErrRestoreInProgress = errors.New("backup: restore already in progress")

// BackupSnapshotReader — согласованное чтение всей переносимой конфигурации
// (§110.4): одна транзакция REPEATABLE READ READ ONLY, креды и секреты
// настроек расшифрованы.
type BackupSnapshotReader interface {
	ReadSnapshot(ctx context.Context) (*backup.Snapshot, error)
}

// BackupTargetState — сводка экземпляра для гейта восстановления и карточки
// выгрузки.
type BackupTargetState struct {
	Nodes int
	Teams int
	Users int
	// APITokens — только не отозванные: отозванные в файл не попадают.
	APITokens int
	// Fresh — признак чистого экземпляра (pg.FreshInstanceSQL).
	Fresh bool
}

// BackupTargetInspector — состояние экземпляра для карточки и предпросмотра.
type BackupTargetInspector interface {
	TargetState(ctx context.Context) (BackupTargetState, error)
}

// RestoreSet — конфигурация, готовая к записи. Перемап id, имена баз
// ClickHouse, статусы узлов и сверка с сид-строками уже выполнены usecase'ом;
// адаптер только пишет — в порядке полей, одной транзакцией.
type RestoreSet struct {
	// TeamUpdates — команды цели, совпавшие по слагу (Default): меняются
	// название, внешняя ссылка и имя базы ClickHouse (по instance.id цели —
	// обычно то же, что уже было); строка и её id остаются.
	TeamUpdates []backup.Team
	Teams       []backup.Team
	Users       []backup.User
	// UserTeams, Favorites, Preferences, Headers, RequestFields,
	// PeerInstances — вставка с пропуском конфликтов (строка уже есть на цели).
	UserTeams   []backup.UserTeam
	Favorites   []backup.Favorite
	Preferences []backup.Preference
	// APITokens — обычная вставка: совпавшие с целью по хешу usecase в набор
	// не кладёт, и конфликт здесь — ошибка, а не штатная ситуация.
	APITokens []backup.APIToken
	// CHTemplates — upsert по id: совпавший по имени шаблон цели несёт id цели.
	// DefaultTemplateID — какой шаблон станет «по умолчанию» (пусто — не менять).
	CHTemplates       []backup.CHTemplate
	DefaultTemplateID string
	NodeGroups        []backup.NodeGroup
	Hosts             []backup.Host
	Headers           []backup.CatalogEntry
	RequestFields     []backup.CatalogEntry
	// LogMasks заменяют набор цели целиком (ReplaceLogMasks=true).
	LogMasks        []backup.LogMask
	ReplaceLogMasks bool
	PeerInstances   []backup.PeerInstance
	// Nodes вставляются с сохранением id и дат.
	Nodes     []*domain.Node
	NodeHosts []backup.NodeHost
	// AppSettings — итог слияния с настройками цели; nil — не менять.
	AppSettings *domain.AppSettings
	// Audit — запись config.import; пишется в той же транзакции.
	Audit *domain.AuditEntry
}

// BackupRestorer — запись RestoreSet (§110.6). Реализация берёт блокировку
// LOCK TABLE teams, nodes, пересчитывает признак чистоты (ErrTargetNotClean),
// пишет набор и отмечает instance_identity.ch_claimed. dryRun — та же запись с
// откатом: предпросмотр видит нарушения ограничений и внешних ключей до
// применения.
type BackupRestorer interface {
	Restore(ctx context.Context, set *RestoreSet, dryRun bool) error
}

// CHOwnership — состояние базы ClickHouse по маркеру владения §70 глазами
// восстановления.
type CHOwnership string

const (
	CHOwnershipNone      CHOwnership = "none"      // базы нет
	CHOwnershipOwned     CHOwnership = "owned"     // маркер этой ноды
	CHOwnershipUnclaimed CHOwnership = "unclaimed" // база есть, маркера нет
	CHOwnershipForeign   CHOwnership = "foreign"   // маркер другой ноды
	CHOwnershipConflict  CHOwnership = "conflict"  // в маркере несколько нод
	CHOwnershipUnknown   CHOwnership = "unknown"   // определить не удалось
)

// CHDatabaseState — состояние базы команды на цели.
type CHDatabaseState struct {
	Ownership CHOwnership
	// Tables — число таблиц в базе без маркера владения.
	Tables int
	// Owner — идентификатор ноды из чужого маркера (диагностика).
	Owner string
}

// CHDatabaseInspector — опрос ClickHouse для плана восстановления (только
// чтение).
type CHDatabaseInspector interface {
	InspectDatabase(ctx context.Context, db string) (CHDatabaseState, error)
	TableExists(ctx context.Context, table string) (bool, error)
}

// BackupRestoreLocker — одна операция восстановления на все реплики Web
// (§110.5, §93). Замок держится всё время предпросмотра или применения,
// включая фазу ClickHouse, которую транзакция записи в PostgreSQL не
// покрывает.
type BackupRestoreLocker interface {
	// LockRestore не ждёт: замок занят — ErrRestoreInProgress. release
	// снимает замок и вызывается ровно один раз.
	LockRestore(ctx context.Context) (release func(), err error)
}

// CHSchemaMaintainer доводит уже существующие таблицы логов до текущей схемы
// (те же ADD COLUMN IF NOT EXISTS, что на старте сервисов, §37/§39/§42.10/§67).
// В отличие от старта, ошибка любой таблицы возвращается: остальные таблицы
// при этом всё равно доводятся (операции идемпотентны).
type CHSchemaMaintainer interface {
	EnsureColumns(ctx context.Context, tables []string) error
}
