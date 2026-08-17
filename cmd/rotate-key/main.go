// rotate-key — перешифровывает чувствительные данные со старого ключа на новый
// (§5.5, §90.4): секретные колонки nodes (auth_credentials,
// incoming_auth_credentials, rmq_password) и секреты app_settings (DSN Sentry,
// пароли ClickHouse и SMTP, токен Telegram-бота).
//
// Запуск (параметры — через окружение, см. ниже):
//
//	OLD_KEY=<base64> NEW_KEY=<base64> [DRY_RUN=true] rotate-key [--config=...]
//
// Ключи и режим передаются переменными окружения, а не флагами, потому что
// разбор командной строки принадлежит платформенному bootstrap.Init: он парсит
// os.Args своим FlagSet с ExitOnError и на любом «чужом» флаге печатает usage и
// завершает процесс. Утилита с флагами --old-key/--new-key поэтому не
// запускалась вовсе (§90.4, найдено прогоном процедуры на стенде).
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
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/crypto"
	"nexus/internal/platform/keyrotate"
	"nexus/internal/platform/logging"
)

const projectName = "rotate-key"

// isTrue — мягкий разбор булевой переменной окружения: пустая строка и любое
// «не да» означают выключено (флаг задаётся людьми в командной строке).
func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}

// versionInfoData — как у остальных бинарей: bootstrap.Init разбирает его через
// build.NewOption. Без файла утилита падала на старте с «build option: parse
// versioninfo.json: unexpected end of JSON input» (передавался nil), то есть
// процедура ротации ключа из DEPLOYMENT §12 не выполнялась в принципе.
//
//go:embed versioninfo.json
var versionInfoData []byte

func main() {
	oldKey := os.Getenv("OLD_KEY")
	newKey := os.Getenv("NEW_KEY")
	dryRun := isTrue(os.Getenv("DRY_RUN"))

	if oldKey == "" || newKey == "" {
		fmt.Fprintln(os.Stderr, "both OLD_KEY and NEW_KEY environment variables are required")
		fmt.Fprintln(os.Stderr, "usage: OLD_KEY=<base64> NEW_KEY=<base64> [DRY_RUN=true] rotate-key [--config=path]")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	_, _, cfg, logger, _ := bootstrap.Init(versionInfoData, projectName)
	defer bootstrap.Shutdown(logger)

	oldC, err := crypto.NewCipher(oldKey)
	if err != nil {
		logger.ErrorWithOp("old key invalid", err, "rotate-key.main")
		os.Exit(1)
	}
	newC, err := crypto.NewCipher(newKey)
	if err != nil {
		logger.ErrorWithOp("new key invalid", err, "rotate-key.main")
		os.Exit(1)
	}

	pool := bootstrap.MustPG(ctx, cfg, logger)
	defer pool.Close()

	var st keyrotate.Stats

	// Отчёт печатается и на аварийном выходе: после прерванного прогона первое,
	// что нужно оператору, — сколько строк УЖЕ перешифровано новым ключом
	// (от этого зависит, повторять прогон или возвращать старый ключ).
	nodeStats, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, dryRun, logger)
	st.Add(nodeStats)
	if err != nil {
		logger.ErrorWithOp("rotate nodes failed", err, "rotate-key.nodes")
		report(logger, st, dryRun)
		os.Exit(1)
	}

	appStats, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, dryRun, logger)
	st.Add(appStats)
	if err != nil {
		logger.ErrorWithOp("rotate app_settings failed", err, "rotate-key.app_settings")
		report(logger, st, dryRun)
		os.Exit(1)
	}

	report(logger, st, dryRun)
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
