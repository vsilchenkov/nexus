package usecase

import (
	"context"
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

// BackupDeps — зависимости BackupUsecase.
type BackupDeps struct {
	Snapshots port.BackupSnapshotReader
	Target    port.BackupTargetInspector
	Audit     *AuditUsecase
	Source    BackupSource
	Logger    logging.Logger
}

// BackupUsecase — резервная копия конфигурации (§110): выгрузка в файл,
// зашифрованный паролем, и восстановление на чистом экземпляре.
type BackupUsecase struct {
	snapshots port.BackupSnapshotReader
	target    port.BackupTargetInspector
	audit     *AuditUsecase
	source    BackupSource
	logger    logging.Logger

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
		snapshots: d.Snapshots,
		target:    d.Target,
		audit:     d.Audit,
		source:    d.Source,
		logger:    d.Logger,
		kdf:       backupfile.DefaultKDF(),
		clock:     clock.System(),
		heavy:     make(chan struct{}, 1),
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
// отказывает по ctx. Возвращает функцию освобождения.
func (u *BackupUsecase) acquire(ctx context.Context) (func(), error) {
	select {
	case u.heavy <- struct{}{}:
		return func() { <-u.heavy }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("backup: wait for running operation: %w", ctx.Err())
	}
}
