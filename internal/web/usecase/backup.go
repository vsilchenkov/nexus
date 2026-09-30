package usecase

import (
	"context"
	"errors"
	"fmt"

	"nexus/internal/domain"
	"nexus/internal/platform/backupfile"
	"nexus/internal/platform/clock"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// BackupSource — сведения об этой ноде для заголовка файла резервной копии и
// карточки «Резервная копия» (§110).
type BackupSource struct {
	NexusVersion  string
	SchemaVersion uint
	InstanceID    domain.InstanceID
	// CHAvailable — ClickHouse подключён при старте Web. Без него
	// восстановление не создаст базы и таблицы команд, поэтому импорт
	// блокируется; выгрузке ClickHouse не нужен.
	CHAvailable bool
}

// backupSettingsImporter — подготовка глобальных настроек из файла и
// публикация их перезагрузки (AppSettingsUsecase, §110.7).
type backupSettingsImporter interface {
	PrepareImport(ctx context.Context, patch *domain.AppSettings) (*domain.AppSettings, []string, []SettingsFieldSkip, error)
	PublishSections(ctx context.Context, actorID string, sections []string)
}

// BackupDeps — зависимости BackupUsecase.
type BackupDeps struct {
	Snapshots port.BackupSnapshotReader
	Target    port.BackupTargetInspector
	Restorer  port.BackupRestorer
	// Locker — замок восстановления на все реплики; nil — только слот
	// процесса (тесты).
	Locker port.BackupRestoreLocker
	// CHInspector — nil, если ClickHouse не подключён: восстановление тогда
	// недоступно (Source.CHAvailable=false).
	CHInspector   port.CHDatabaseInspector
	CHProvisioner backupCHProvisioner
	CHSchema      port.CHSchemaMaintainer
	Settings      backupSettingsImporter
	Audit         *AuditUsecase
	Source        BackupSource
	// SelfIngressHosts — свои authority (§32.2): узел, чей target_url ведёт на
	// вход самой ноды, восстанавливается выключенным.
	SelfIngressHosts []string
	// NodesHardLimit — потолок узлов на команду (§3.3); 0 — без потолка.
	NodesHardLimit int
	Logger         logging.Logger
}

// BackupUsecase — резервная копия конфигурации (§110): выгрузка в файл,
// зашифрованный паролем, и восстановление на чистом экземпляре.
type BackupUsecase struct {
	snapshots        port.BackupSnapshotReader
	target           port.BackupTargetInspector
	restorer         port.BackupRestorer
	locker           port.BackupRestoreLocker
	chInspector      port.CHDatabaseInspector
	chProvisioner    backupCHProvisioner
	chSchema         port.CHSchemaMaintainer
	settings         backupSettingsImporter
	audit            *AuditUsecase
	source           BackupSource
	selfIngressHosts []string
	nodesHardLimit   int
	logger           logging.Logger

	kdf   backupfile.KDFParams
	clock clock.Clock
	// heavy — одна операция с файлом за раз. Вывод ключа Argon2id берёт
	// 64 МиБ памяти, снимок и файл — ещё столько же: нажатая дважды кнопка или
	// два администратора не должны складывать это в памяти Web.
	heavy chan struct{}
}

// BackupOption — необязательная настройка BackupUsecase.
type BackupOption func(*BackupUsecase)

// WithBackupKDF задаёт параметры вывода ключа для новых файлов. Тестам —
// дешёвые параметры; в работе — backupfile.DefaultKDF.
func WithBackupKDF(k backupfile.KDFParams) BackupOption {
	return func(u *BackupUsecase) { u.kdf = k }
}

// WithBackupClock подменяет источник времени (метка файла и его имя).
func WithBackupClock(c clock.Clock) BackupOption {
	return func(u *BackupUsecase) { u.clock = c }
}

func NewBackupUsecase(d BackupDeps, opts ...BackupOption) *BackupUsecase {
	u := &BackupUsecase{
		snapshots:        d.Snapshots,
		target:           d.Target,
		restorer:         d.Restorer,
		locker:           d.Locker,
		chInspector:      d.CHInspector,
		chProvisioner:    d.CHProvisioner,
		chSchema:         d.CHSchema,
		settings:         d.Settings,
		audit:            d.Audit,
		source:           d.Source,
		selfIngressHosts: d.SelfIngressHosts,
		nodesHardLimit:   d.NodesHardLimit,
		logger:           d.Logger,
		kdf:              backupfile.DefaultKDF(),
		clock:            clock.System(),
		heavy:            make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// BackupStatus — состояние экземпляра для карточки «Резервная копия».
type BackupStatus struct {
	port.BackupTargetState
	CHAvailable   bool
	InstanceID    string
	NexusVersion  string
	SchemaVersion uint
}

// Status — счётчики для карточки выгрузки и признаки, от которых зависит
// доступность восстановления.
func (u *BackupUsecase) Status(ctx context.Context) (*BackupStatus, error) {
	st, err := u.target.TargetState(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup status: %w", err)
	}
	return &BackupStatus{
		BackupTargetState: st,
		CHAvailable:       u.source.CHAvailable,
		InstanceID:        u.source.InstanceID.String(),
		NexusVersion:      u.source.NexusVersion,
		SchemaVersion:     u.source.SchemaVersion,
	}, nil
}

// acquire занимает слот тяжёлой операции; ждёт, пока освободится, или
// отказывает по ctx. Возвращает функцию освобождения. Для выгрузки.
func (u *BackupUsecase) acquire(ctx context.Context) (func(), error) {
	select {
	case u.heavy <- struct{}{}:
		return func() { <-u.heavy }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("backup: wait for running operation: %w", ctx.Err())
	}
}

// acquireRestore — слот для предпросмотра или применения. Не ждёт: второй
// импорт сразу получает «импорт уже идёт» (§110.5), а не висит до конца
// первого, чтобы затем упереться в «экземпляр не чистый». Поверх слота
// процесса — замок на все реплики Web за балансировщиком (§93) на всё время
// операции: advisory-замок транзакции Restore не покрывает фазу ClickHouse,
// и две реплики создавали бы базы и таблицы одновременно.
func (u *BackupUsecase) acquireRestore(ctx context.Context) (func(), error) {
	select {
	case u.heavy <- struct{}{}:
	default:
		u.logger.Debug("backup import: another backup operation is running in this process")
		return nil, port.ErrRestoreInProgress
	}
	if u.locker == nil {
		return func() { <-u.heavy }, nil
	}
	unlock, err := u.locker.LockRestore(ctx)
	if err != nil {
		<-u.heavy
		if errors.Is(err, port.ErrRestoreInProgress) {
			u.logger.Debug("backup import: another restore is running on another replica")
		}
		return nil, err
	}
	return func() {
		unlock()
		<-u.heavy
	}, nil
}
