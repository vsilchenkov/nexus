// rotate-key — перешифровывает чувствительные поля nodes (auth_credentials,
// incoming_auth_credentials) со старого ключа на новый (§5.5 ТЗ).
//
// Запуск:
//
//	rotate-key --old-key=<base64> --new-key=<base64> [--config=...] [--dry-run]
//
// Идемпотентен: если значение уже расшифровывается новым ключом — пропускаем
// (повторный запуск безопасен, например, после прерывания посередине).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/config"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/logging"
)

const projectName = "rotate-key"

func main() {
	oldKey := flag.String("old-key", "", "old base64-encoded encryption key (32 bytes)")
	newKey := flag.String("new-key", "", "new base64-encoded encryption key (32 bytes)")
	dryRun := flag.Bool("dry-run", false, "do not write back; just report planned changes")
	flag.Parse()

	if *oldKey == "" || *newKey == "" {
		fmt.Fprintln(os.Stderr, "both --old-key and --new-key are required")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	_, _, cfg, logger, _ := bootstrap.Init(nil, projectName)
	defer bootstrap.Shutdown(logger)

	oldC, err := crypto.NewCipher(*oldKey)
	if err != nil {
		logger.ErrorWithOp("old key invalid", err, "rotate-key.main")
		os.Exit(1)
	}
	newC, err := crypto.NewCipher(*newKey)
	if err != nil {
		logger.ErrorWithOp("new key invalid", err, "rotate-key.main")
		os.Exit(1)
	}

	st, err := rotateAll(ctx, cfg, oldC, newC, *dryRun, logger)
	if err != nil {
		logger.ErrorWithOp("rotate failed", err, "rotate-key.rotateAll")
		os.Exit(1)
	}

	logger.Info("rotate-key finished",
		logger.Int("rows_scanned", st.scanned),
		logger.Int("rows_reencrypted", st.reencrypted),
		logger.Int("rows_skipped_already_new", st.skippedAlreadyNew),
		logger.Int("rows_empty", st.empty),
		logger.Any("dry_run", *dryRun))
}

type stats struct {
	scanned           int
	reencrypted       int
	skippedAlreadyNew int
	empty             int
}

func rotateAll(
	ctx context.Context,
	cfg *config.Config,
	oldC, newC *crypto.Cipher,
	dryRun bool,
	logger logging.Logger,
) (stats, error) {
	pool := bootstrap.MustPG(ctx, cfg, logger)
	defer pool.Close()

	var st stats
	for _, col := range []string{"auth_credentials", "incoming_auth_credentials"} {
		colStats, err := rotateColumn(ctx, pool, oldC, newC, col, dryRun, logger)
		if err != nil {
			return st, err
		}
		st.scanned += colStats.scanned
		st.reencrypted += colStats.reencrypted
		st.skippedAlreadyNew += colStats.skippedAlreadyNew
		st.empty += colStats.empty
	}
	return st, nil
}

func rotateColumn(
	ctx context.Context,
	pool *pgxpool.Pool,
	oldC, newC *crypto.Cipher,
	col string,
	dryRun bool,
	logger logging.Logger,
) (stats, error) {
	var st stats
	rows, err := pool.Query(ctx, "SELECT id, "+col+" FROM nodes WHERE "+col+" <> ''")
	if err != nil {
		return st, fmt.Errorf("select %s: %w", col, err)
	}
	type row struct {
		id  string
		val string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.val); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan %s: %w", col, err)
		}
		batch = append(batch, r)
	}
	rows.Close()

	for _, r := range batch {
		st.scanned++
		if r.val == "" {
			st.empty++
			continue
		}
		if _, err := newC.Decrypt(r.val); err == nil {
			st.skippedAlreadyNew++
			continue
		}
		plain, err := oldC.Decrypt(r.val)
		if err != nil {
			return st, fmt.Errorf("decrypt %s/%s with old key: %w", col, r.id, err)
		}
		next, err := newC.Encrypt(plain)
		if err != nil {
			return st, fmt.Errorf("encrypt %s/%s with new key: %w", col, r.id, err)
		}
		if dryRun {
			st.reencrypted++
			continue
		}
		if _, err := pool.Exec(ctx, "UPDATE nodes SET "+col+" = $1 WHERE id = $2", next, r.id); err != nil {
			return st, fmt.Errorf("update %s/%s: %w", col, r.id, err)
		}
		st.reencrypted++
		logger.Info("re-encrypted",
			logger.Str("column", col),
			logger.Str("id", r.id))
	}
	return st, nil
}
