package usecase

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"nexus/internal/platform/reloader"
)

// backupApplyTimeout — потолок применения. Сотни CREATE TABLE и одна
// транзакция PostgreSQL укладываются в секунды; потолок нужен, чтобы
// отвязанный от клиента контекст не висел вечно на недоступном ClickHouse.
const backupApplyTimeout = 10 * time.Minute

// Отказы применения.
var (
	// ErrBackupConfirmHost — введённый адрес не совпал с адресом сервера.
	ErrBackupConfirmHost = errors.New("backup: confirmation host does not match this server")
	// ErrBackupDigestMismatch — применяется не тот файл, не те опции, или
	// план, пересобранный по текущему состоянию цели и ClickHouse, уже не тот,
	// что был показан в предпросмотре.
	ErrBackupDigestMismatch = errors.New("backup: file, options or plan differ from the preview")
	// ErrBackupBlocked — план на момент применения содержит блокирующие
	// проблемы (состояние ClickHouse или цели изменилось после предпросмотра).
	ErrBackupBlocked = errors.New("backup: import plan has blocking problems")
	// ErrBackupCHProvision — не удалось создать базу или таблицу ClickHouse;
	// PostgreSQL не тронут, повтор безопасен.
	ErrBackupCHProvision = errors.New("backup: clickhouse provisioning failed")
)

// backupCHProvisioner — создание баз и таблиц команд (TeamProvisionerCH:
// база занимается маркером владения §70, таблица — только в своей базе).
type backupCHProvisioner interface {
	CreateDatabase(ctx context.Context, name string) error
	CreateTable(ctx context.Context, table, ddl string) error
}

// BackupApplyRequest — применение: тот же файл и опции, что в предпросмотре,
// плюс его дайджест и введённый администратором адрес сервера.
type BackupApplyRequest struct {
	BackupImportRequest
	Digest      string
	ConfirmHost string
}

// BackupImportResult — итог восстановления.
type BackupImportResult struct {
	Preview *BackupImportPreview `json:"preview"`
	// DatabasesClaimed — базы, созданные или занятые (без уже своих).
	DatabasesClaimed int   `json:"databases_claimed"`
	TablesCreated    int   `json:"tables_created"`
	TablesUpdated    int   `json:"tables_updated"`
	DurationMs       int64 `json:"duration_ms"`
}

// ApplyImport восстанавливает конфигурацию (§110.8): пересобирает план и
// сверяет его с предпросмотром, создаёт базы и таблицы ClickHouse, одной
// транзакцией пишет PostgreSQL, публикует перезагрузку настроек.
//
// Работает на контексте, отвязанном от соединения клиента: закрытая вкладка
// не должна оборвать восстановление между ClickHouse и PostgreSQL.
func (u *BackupUsecase) ApplyImport(ctx context.Context, actor Actor, req BackupApplyRequest) (*BackupImportResult, error) {
	if !sameHostname(req.ConfirmHost, req.SelfHost) {
		return nil, ErrBackupConfirmHost
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backupApplyTimeout)
	defer cancel()
	release, err := u.acquireRestore(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	started := u.clock.Now()
	plan, err := u.planImport(ctx, actor, req.BackupImportRequest)
	if err != nil {
		return nil, err
	}
	if plan.preview.Digest != req.Digest {
		return nil, ErrBackupDigestMismatch
	}
	if len(plan.preview.Blockers) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrBackupBlocked, plan.preview.Blockers[0].Code)
	}
	res, err := u.provisionClickHouse(ctx, plan)
	if err != nil {
		return nil, err
	}
	if err := u.restorer.Restore(ctx, plan.set, false); err != nil {
		return nil, fmt.Errorf("backup import: write: %w", err)
	}
	// Маски — отдельная секция: Sender перечитывает их без рестарта (§95).
	// SectionAll не публикуется — он переподключил бы ClickHouse во всех
	// сервисах.
	u.settings.PublishSections(ctx, actor.UserID,
		append(slices.Clone(plan.preview.SettingsSections), string(reloader.SectionMasking)))
	res.Preview = plan.preview
	res.DurationMs = u.clock.Now().Sub(started).Milliseconds()
	u.logger.Info("configuration restored from backup",
		u.logger.Str("source_instance", plan.preview.Header.SourceInstanceID),
		u.logger.Int("nodes", len(plan.set.Nodes)),
		u.logger.Int("teams", len(plan.set.Teams)+len(plan.set.TeamUpdates)),
		u.logger.Int("tables_created", res.TablesCreated),
		u.logger.Int("duration_ms", int(res.DurationMs)))
	return res, nil
}

// provisionClickHouse — базы и таблицы до записи в PostgreSQL. Все операции
// идемпотентны (захват своей базы, CREATE TABLE IF NOT EXISTS, ADD COLUMN IF
// NOT EXISTS): сбой здесь не оставляет следов в PostgreSQL, повтор безопасен.
func (u *BackupUsecase) provisionClickHouse(ctx context.Context, plan *backupImportPlan) (*BackupImportResult, error) {
	res := &BackupImportResult{}
	for _, d := range plan.databases {
		if err := u.chProvisioner.CreateDatabase(ctx, d.Name); err != nil {
			return nil, fmt.Errorf("%w: database %s: %w", ErrBackupCHProvision, d.Name, err)
		}
		if d.Action != chActionReuse {
			res.DatabasesClaimed++
		}
	}
	var existing []string
	for _, t := range plan.tables {
		if t.exists {
			existing = append(existing, t.table)
			continue
		}
		if err := u.chProvisioner.CreateTable(ctx, t.table, t.ddl); err != nil {
			return nil, fmt.Errorf("%w: table %s: %w", ErrBackupCHProvision, t.table, err)
		}
		res.TablesCreated++
	}
	if len(existing) > 0 {
		// Уцелевшие таблицы доводятся до текущей схемы — иначе чтение логов
		// падало бы до рестарта сервисов (те же ALTER, что на старте). Сбой
		// прерывает импорт до записи в PostgreSQL, как и сбой создания.
		if err := u.chSchema.EnsureColumns(ctx, existing); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBackupCHProvision, err)
		}
		res.TablesUpdated = len(existing)
	}
	u.logger.Debug("backup import: clickhouse provisioned",
		u.logger.Int("databases", len(plan.databases)), u.logger.Int("tables_created", res.TablesCreated),
		u.logger.Int("tables_updated", res.TablesUpdated))
	return res, nil
}

// sameHostname — введённый адрес совпадает с адресом, по которому пришёл
// запрос. Сверяется имя хоста без порта и регистра: прокси перед Web может
// убрать порт из Host. Это подтверждение «вы на том сервере», а не
// аутентификация — её обеспечивают роль admin и браузерная сессия.
func sameHostname(confirm, requestHost string) bool {
	c, r := hostnameOnly(confirm), hostnameOnly(requestHost)
	return c != "" && c == r
}

func hostnameOnly(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if host, _, err := net.SplitHostPort(h); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(h, "[]")
}
