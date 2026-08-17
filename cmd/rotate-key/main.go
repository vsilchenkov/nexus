// rotate-key — перешифровывает чувствительные данные со старого ключа на новый
// (§5.5, §90.4): секретные колонки nodes (auth_credentials,
// incoming_auth_credentials, rmq_password) и секреты app_settings (DSN Sentry,
// пароли ClickHouse и SMTP, токен Telegram-бота).
//
// Запуск:
//
//	rotate-key --old-key=<base64> --new-key=<base64> [--config=...] [--dry-run]
//
// Идемпотентен: если значение уже расшифровывается новым ключом — пропускаем
// (повторный запуск безопасен, например, после прерывания посередине).
//
// Значения, лежащие открытым текстом (данные старше включения шифрования),
// не ошибка: они шифруются новым ключом и считаются отдельным счётчиком
// upgraded_plaintext. Поэтому прогон с OLD_KEY == NEW_KEY работает как разовая
// миграция исторических plaintext-значений. Ошибкой остаётся только шифротекст,
// который не бьётся старым ключом.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/keyrotate"
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

	pool := bootstrap.MustPG(ctx, cfg, logger)
	defer pool.Close()

	var st keyrotate.Stats

	nodeStats, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, *dryRun, logger)
	st.Add(nodeStats)
	if err != nil {
		logger.ErrorWithOp("rotate nodes failed", err, "rotate-key.nodes")
		os.Exit(1)
	}

	appStats, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, *dryRun, logger)
	st.Add(appStats)
	if err != nil {
		logger.ErrorWithOp("rotate app_settings failed", err, "rotate-key.app_settings")
		os.Exit(1)
	}

	report(logger, st, *dryRun)
}

func report(logger logging.Logger, st keyrotate.Stats, dryRun bool) {
	logger.Info("rotate-key finished",
		logger.Int("rows_scanned", st.Scanned),
		logger.Int("rows_reencrypted", st.Reencrypted),
		logger.Int("rows_upgraded_plaintext", st.UpgradedPlaintext),
		logger.Int("rows_skipped_already_new", st.SkippedAlreadyNew),
		logger.Int("rows_empty", st.Empty),
		logger.Any("dry_run", dryRun))
}
