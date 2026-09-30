package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"nexus/internal/domain/backup"
	"nexus/internal/platform/backupfile"
	"nexus/internal/web/usecase/port"
)

// Отказы восстановления, которые приходят до построения плана.
var (
	// ErrBackupCHUnavailable — ClickHouse не подключён: базы и таблицы
	// команд создать негде (§110.6).
	ErrBackupCHUnavailable = errors.New("backup: clickhouse is not connected")
	// ErrBackupSchemaNewer — файл выгружен с более новой схемы, чем у этой
	// ноды: колонки, которых здесь нет, потерялись бы молча.
	ErrBackupSchemaNewer = errors.New("backup: file schema is newer than this node")
	// ErrBackupOptionsInvalid — неизвестный режим статусов узлов.
	ErrBackupOptionsInvalid = errors.New("backup: invalid import options")
)

// BackupStatusMode — статусы узлов после восстановления (§110.6).
type BackupStatusMode string

const (
	// BackupStatusPausePull — как в файле, узлы RabbitMQAsync — на паузе
	// (по умолчанию): живой источник и цель не разбирают одну очередь.
	BackupStatusPausePull BackupStatusMode = "pause_pull"
	// BackupStatusAsFile — всё как в файле.
	BackupStatusAsFile BackupStatusMode = "as_file"
	// BackupStatusPauseAll — все включённые узлы на паузе.
	BackupStatusPauseAll BackupStatusMode = "pause_all"
)

// BackupImportOptions — выбор администратора в предпросмотре. Входит в
// preview_digest: применяется ровно то, что было показано.
type BackupImportOptions struct {
	StatusMode BackupStatusMode `json:"status_mode"`
	// AdoptUnclaimed — занять существующие непустые базы без маркера владения.
	AdoptUnclaimed bool `json:"adopt_unclaimed"`
}

// BackupImportRequest — файл и выбор администратора.
type BackupImportRequest struct {
	File       []byte
	Passphrase string
	Options    BackupImportOptions
	// SelfHost — адрес, по которому администратор открыл Web (Host запроса):
	// инстанс §73 с этим адресом — сама нода, в реестр не переносится.
	SelfHost string
}

// BackupIssue — предупреждение, пропуск или блокирующая проблема плана.
// Code — ключ перевода в интерфейсе, Subject — о чём (узел, база, поле),
// Detail — техническая причина.
type BackupIssue struct {
	Code    string `json:"code"`
	Subject string `json:"subject,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// BackupEntityPlan — судьба строк одной сущности.
type BackupEntityPlan struct {
	Create   int `json:"create"`
	Existing int `json:"existing"`
	// Replace — строки цели, которые заменяются (маски логов).
	Replace int `json:"replace,omitempty"`
}

// Действия с базой ClickHouse команды.
const (
	chActionCreate  = "create"  // базы нет — создать
	chActionReuse   = "reuse"   // наша база — использовать
	chActionClaim   = "claim"   // без маркера и пустая — занять
	chActionAdopt   = "adopt"   // без маркера, с таблицами — занять по галочке
	chActionBlocked = "blocked" // чужая, конфликт, неизвестно, или не отмечена галочка
)

// BackupCHDatabasePlan — решение по базе команды.
type BackupCHDatabasePlan struct {
	Team      string `json:"team"`
	Name      string `json:"name"`
	Ownership string `json:"ownership"`
	Tables    int    `json:"tables"`
	Action    string `json:"action"`
}

// BackupCHTablesPlan — таблицы логов узлов.
type BackupCHTablesPlan struct {
	Create   int `json:"create"`
	Existing int `json:"existing"`
	// External — внешние таблицы §64 (Nexus их не создаёт); ExternalMissing —
	// из них не найденные на сервере.
	External        int `json:"external"`
	ExternalMissing int `json:"external_missing"`
	// Unmanaged — таблицы вне баз команд: не создаются, остаются как есть.
	Unmanaged int `json:"unmanaged"`
}

// BackupNodeStatusPlan — статусы узлов после восстановления.
type BackupNodeStatusPlan struct {
	Enabled  int `json:"enabled"`
	Paused   int `json:"paused"`
	Disabled int `json:"disabled"`
}

// BackupImportPreview — план восстановления для администратора (§110.8).
type BackupImportPreview struct {
	Header           backupfile.Header           `json:"header"`
	ExportedBy       string                      `json:"exported_by"`
	SourcePublicURL  string                      `json:"source_public_url"`
	Entities         map[string]BackupEntityPlan `json:"entities"`
	Skipped          []BackupIssue               `json:"skipped"`
	Databases        []BackupCHDatabasePlan      `json:"databases"`
	Tables           BackupCHTablesPlan          `json:"tables"`
	NodeStatuses     BackupNodeStatusPlan        `json:"node_statuses"`
	SettingsSections []string                    `json:"settings_sections"`
	Warnings         []BackupIssue               `json:"warnings"`
	Blockers         []BackupIssue               `json:"blockers"`
	// Digest — sha256 файла, опций и плана (planDigest): применение сверяет
	// его, чтобы записать ровно показанный план.
	Digest string `json:"digest"`
}

// chTableAction — таблица логов, которую фаза ClickHouse создаёт или доводит
// до текущей схемы.
type chTableAction struct {
	table  string
	ddl    string
	exists bool
}

// backupImportPlan — предпросмотр плюс всё, что нужно применению.
type backupImportPlan struct {
	preview   *BackupImportPreview
	set       *port.RestoreSet
	databases []BackupCHDatabasePlan
	tables    []chTableAction
}

// PreviewImport расшифровывает файл, строит план восстановления и
// выполняет всю запись в PostgreSQL с откатом (dry-run): нарушения ограничений
// видны до применения. ClickHouse только опрашивается.
func (u *BackupUsecase) PreviewImport(ctx context.Context, actor Actor, req BackupImportRequest) (*BackupImportPreview, error) {
	release, err := u.acquireRestore(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	plan, err := u.planImport(ctx, actor, req)
	if err != nil {
		return nil, err
	}
	if len(plan.preview.Blockers) > 0 {
		u.logger.Debug("backup preview: blocked, dry-run skipped",
			u.logger.Int("blockers", len(plan.preview.Blockers)))
		return plan.preview, nil
	}
	if err := u.restorer.Restore(ctx, plan.set, true); err != nil {
		if errors.Is(err, port.ErrTargetNotClean) || errors.Is(err, port.ErrRestoreInProgress) {
			return nil, err
		}
		u.logger.Debug("backup preview: dry-run failed", u.logger.Err(err))
		plan.preview.Blockers = append(plan.preview.Blockers, BackupIssue{Code: "dry_run_failed", Detail: err.Error()})
	}
	return plan.preview, nil
}

// planImport — гейт чистоты, расшифровка, сверка с целью и план. Ничего не
// пишет.
func (u *BackupUsecase) planImport(ctx context.Context, actor Actor, req BackupImportRequest) (*backupImportPlan, error) {
	mode, err := normalizeStatusMode(req.Options.StatusMode)
	if err != nil {
		return nil, err
	}
	req.Options.StatusMode = mode
	if err := u.checkRestorable(ctx); err != nil {
		return nil, err
	}
	h, src, err := u.openBackup(req.File, req.Passphrase)
	if err != nil {
		return nil, err
	}
	tgt, err := u.snapshots.ReadSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup import: read target: %w", err)
	}
	b := newImportBuilder(u, h, src, tgt, req)
	b.remapIdentity()
	b.remapAccess()
	b.remapCatalogs()
	b.planNodes()
	if err := b.planSettings(ctx); err != nil {
		return nil, err
	}
	if err := b.planClickHouse(ctx); err != nil {
		return nil, err
	}
	b.finish(actor)
	plan := b.plan()
	plan.preview.Digest = backupDigest(req.File, req.Options, plan)
	return plan, nil
}

// checkRestorable — экземпляр чистый и ClickHouse подключён. Проверяется до
// расшифровки: на боевой ноде файл не вскрывается вовсе.
func (u *BackupUsecase) checkRestorable(ctx context.Context) error {
	st, err := u.target.TargetState(ctx)
	if err != nil {
		return fmt.Errorf("backup import: %w", err)
	}
	if !st.Fresh {
		u.logger.Debug("backup import: target is not clean",
			u.logger.Int("nodes", st.Nodes), u.logger.Int("teams", st.Teams))
		return port.ErrTargetNotClean
	}
	if u.chInspector == nil || u.chProvisioner == nil || u.chSchema == nil || !u.source.CHAvailable {
		return ErrBackupCHUnavailable
	}
	return nil
}

// openBackup — схема по открытому заголовку (дёшево, до Argon2id), затем
// расшифровка, повторная сверка по заверенному заголовку и проверка снимка.
func (u *BackupUsecase) openBackup(file []byte, passphrase string) (*backupfile.Header, *backup.Snapshot, error) {
	if h, err := backupfile.ReadHeader(file); err != nil {
		return nil, nil, err
	} else if h.SchemaVersion > u.source.SchemaVersion {
		return nil, nil, fmt.Errorf("%w: file %d, node %d", ErrBackupSchemaNewer, h.SchemaVersion, u.source.SchemaVersion)
	}
	h, payload, err := backupfile.Open(file, passphrase)
	if err != nil {
		return nil, nil, err
	}
	if h.SchemaVersion > u.source.SchemaVersion {
		return nil, nil, fmt.Errorf("%w: file %d, node %d", ErrBackupSchemaNewer, h.SchemaVersion, u.source.SchemaVersion)
	}
	s, err := decodeSnapshot(payload)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, nil, err
	}
	return h, s, nil
}

// decodeSnapshot — строгий разбор: поле, которого этот бинарь не знает,
// означает файл более новой версии Nexus. Схема PostgreSQL при этом может
// совпадать (новый ключ глобальных настроек или спецификации подтверждения
// добавляется без миграции), и нестрогий разбор молча потерял бы его.
// Второй, нестрогий проход отличает «новое поле» от повреждённого снимка без
// разбора текста ошибки encoding/json.
func decodeSnapshot(payload []byte) (*backup.Snapshot, error) {
	var s backup.Snapshot
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	strictErr := dec.Decode(&s)
	if strictErr == nil {
		return &s, nil
	}
	var lenient backup.Snapshot
	if err := json.Unmarshal(payload, &lenient); err != nil {
		return nil, fmt.Errorf("%w: payload: %w", backup.ErrInvalidSnapshot, err)
	}
	return nil, fmt.Errorf("%w: %w", ErrBackupSchemaNewer, strictErr)
}

// planDigest — что закрепляет дайджест помимо файла: опции и весь план, как
// его видел администратор (заголовок файла уже в байтах файла), плюс решение
// по каждой таблице. План строится заново по текущему состоянию цели и
// ClickHouse, поэтому одних файла и опций мало: база, ставшая за это время
// непустой без маркера, сменила бы «занять пустую» на «занять с таблицами»
// под той же галочкой, а новые строки цели — счётчики «создать/есть».
type planDigest struct {
	Options          BackupImportOptions         `json:"options"`
	Entities         map[string]BackupEntityPlan `json:"entities"`
	Skipped          []BackupIssue               `json:"skipped"`
	Databases        []BackupCHDatabasePlan      `json:"databases"`
	Tables           BackupCHTablesPlan          `json:"tables"`
	TableActions     []tableDigest               `json:"table_actions"`
	NodeStatuses     BackupNodeStatusPlan        `json:"node_statuses"`
	SettingsSections []string                    `json:"settings_sections"`
	Warnings         []BackupIssue               `json:"warnings"`
	Blockers         []BackupIssue               `json:"blockers"`
}

type tableDigest struct {
	Table  string `json:"table"`
	Exists bool   `json:"exists"`
}

// backupDigest — sha256 файла и плана. Сериализация детерминирована:
// структуры с фиксированным порядком полей, ключи map сортирует
// encoding/json, срезы идут в порядке файла и снимка цели.
func backupDigest(file []byte, opts BackupImportOptions, plan *backupImportPlan) string {
	p := plan.preview
	d := planDigest{
		Options: opts, Entities: p.Entities, Skipped: p.Skipped, Databases: p.Databases,
		Tables: p.Tables, NodeStatuses: p.NodeStatuses, SettingsSections: p.SettingsSections,
		Warnings: p.Warnings, Blockers: p.Blockers,
	}
	for _, t := range plan.tables {
		d.TableActions = append(d.TableActions, tableDigest{Table: t.table, Exists: t.exists})
	}
	o, _ := json.Marshal(d) // структуры без каналов и функций — ошибки нет
	h := sha256.New()
	h.Write(file)
	h.Write([]byte{0})
	h.Write(o)
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeStatusMode — пустой режим означает режим по умолчанию.
func normalizeStatusMode(m BackupStatusMode) (BackupStatusMode, error) {
	switch m {
	case "":
		return BackupStatusPausePull, nil
	case BackupStatusPausePull, BackupStatusAsFile, BackupStatusPauseAll:
		return m, nil
	default:
		return "", fmt.Errorf("%w: status_mode %q", ErrBackupOptionsInvalid, m)
	}
}
