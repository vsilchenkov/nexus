package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	pgpf "nexus/internal/platform/pg"
	"nexus/internal/web/usecase/port"
)

// backupRestoreLockKey — ключ pg_try_advisory_xact_lock восстановления
// (§110.5): второй импорт — и предпросмотр, и применение — получает «импорт
// уже идёт», а не ждёт на LOCK TABLE. Значение — произвольная константа,
// общая для всех реплик Web на одной базе.
const backupRestoreLockKey int64 = 0x6e657875_0110

var _ port.BackupRestorer = (*BackupRepoPg)(nil)

// restoreStep — одна группа таблиц восстановления.
type restoreStep func(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error

// Restore записывает конфигурацию одной транзакцией (§110.8). Под
// блокировкой LOCK TABLE teams, nodes пересчитывается признак чистого
// экземпляра: узел или команда, созданные параллельно, либо дождутся конца
// восстановления, либо сделают его невозможным — смешаться с ним не могут.
// dryRun — та же запись с откатом: ограничения и внешние ключи проверены,
// в базе ничего не осталось.
func (r *BackupRepoPg) Restore(ctx context.Context, set *port.RestoreSet, dryRun bool) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("backup restore begin: %w", err)
	}
	// После Commit откат — no-op; в dry-run и при ошибке это и есть откат.
	// Его собственная ошибка результат не меняет.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockForRestore(ctx, tx); err != nil {
		return err
	}
	steps := []restoreStep{
		restoreIdentity, restoreAccess, restoreTemplates, restoreCatalogs,
		r.restoreNodes, r.restoreSettings,
	}
	for _, step := range steps {
		if err := step(ctx, tx, set); err != nil {
			return err
		}
	}
	if dryRun {
		r.logger.Debug("backup restore: dry-run passed, rolled back",
			r.logger.Int("nodes", len(set.Nodes)), r.logger.Int("teams", len(set.Teams)))
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("backup restore commit: %w", err)
	}
	return nil
}

// lockForRestore — один импорт за раз, затем блокировка teams и nodes и
// пересчёт признака чистоты под ней.
func lockForRestore(ctx context.Context, tx pgx.Tx) error {
	var got bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, backupRestoreLockKey).Scan(&got); err != nil {
		return fmt.Errorf("backup restore advisory lock: %w", err)
	}
	if !got {
		return port.ErrRestoreInProgress
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("backup restore lock timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE teams, nodes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("backup restore lock tables: %w", err)
	}
	var fresh bool
	if err := tx.QueryRow(ctx, pgpf.FreshInstanceSQL).Scan(&fresh); err != nil {
		return fmt.Errorf("backup restore fresh check: %w", err)
	}
	if !fresh {
		return port.ErrTargetNotClean
	}
	return nil
}

// restoreNodes — узлы с сохранением id и дат (шифрование кредов — то же, что
// у NodeRepoPg), затем связи с хостами (usage_count ведёт триггер).
func (r *BackupRepoPg) restoreNodes(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	repo := NewNodeRepoPg(tx, r.cipher, r.logger)
	for _, n := range set.Nodes {
		if err := repo.InsertPreservingIdentity(ctx, n); err != nil {
			return fmt.Errorf("backup restore node %s: %w", n.ID, err)
		}
	}
	for _, l := range set.NodeHosts {
		if _, err := tx.Exec(ctx, `INSERT INTO node_allowed_hosts (node_id, host_id, created_at)
VALUES ($1::uuid, $2::uuid, $3) ON CONFLICT DO NOTHING`, l.NodeID, l.HostID, l.CreatedAt); err != nil {
			return fmt.Errorf("backup restore node host %s/%s: %w", l.NodeID, l.HostID, err)
		}
	}
	return nil
}

// restoreSettings — глобальные настройки (секреты шифрует AppSettingsRepoPg),
// отметка захвата баз ClickHouse и запись аудита в той же транзакции.
//
// ch_claimed: базы команд заняты фазой ClickHouse до этой транзакции. Без
// флага стартовый гейт §70.5 после рестарта увидел бы «ноду, которая ничего не
// захватывала», хотя узлы у неё уже есть.
func (r *BackupRepoPg) restoreSettings(ctx context.Context, tx pgx.Tx, set *port.RestoreSet) error {
	if set.AppSettings != nil {
		if err := NewAppSettingsRepoPg(tx, r.cipher, r.logger).Update(ctx, set.AppSettings); err != nil {
			return fmt.Errorf("backup restore app_settings: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE instance_identity SET ch_claimed = true WHERE id = 1`); err != nil {
		return fmt.Errorf("backup restore ch_claimed: %w", err)
	}
	if set.Audit != nil {
		if err := NewAuditRepoPg(tx, r.logger).Write(ctx, set.Audit); err != nil {
			return fmt.Errorf("backup restore audit: %w", err)
		}
	}
	return nil
}
