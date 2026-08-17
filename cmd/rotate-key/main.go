// rotate-key — работа с шифрованием чувствительных данных в PostgreSQL
// (§5.5, §90.4, §90.6): секретные колонки nodes (auth_credentials,
// incoming_auth_credentials, rmq_password) и секреты app_settings (DSN Sentry,
// пароли ClickHouse и SMTP, токен Telegram-бота).
//
// Три режима (MODE):
//
//	rotate  — по умолчанию: перешифровать со старого ключа на новый;
//	encrypt — зашифровать то, что лежит открытым текстом, текущим ключом
//	          (разовая миграция данных §90.1; NEW_KEY не нужен);
//	decrypt — обратный ход: расшифровать всё и оставить ОТКРЫТЫМ ТЕКСТОМ.
//	          Нужен только для отката кода на версию без §90.1 — она не понимает
//	          шифротекст и приняла бы его за сам секрет.
//
// Запуск (параметры — через окружение, см. ниже):
//
//	OLD_KEY=<base64> NEW_KEY=<base64> [DRY_RUN=true] rotate-key [--config=...]
//	MODE=encrypt OLD_KEY=<base64> rotate-key
//	MODE=decrypt OLD_KEY=<base64> rotate-key
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

// Режимы работы (переменная MODE).
const (
	modeRotate  = "rotate"
	modeEncrypt = "encrypt"
	modeDecrypt = "decrypt"
)

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
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MODE")))
	if mode == "" {
		mode = modeRotate
	}
	opts := keyrotate.Options{DryRun: isTrue(os.Getenv("DRY_RUN")), Decrypt: mode == modeDecrypt}

	switch mode {
	case modeRotate:
		if oldKey == "" || newKey == "" {
			usage("MODE=rotate requires both OLD_KEY and NEW_KEY")
		}
	case modeEncrypt, modeDecrypt:
		if oldKey == "" {
			usage("MODE=" + mode + " requires OLD_KEY (the key the data is encrypted with)")
		}
		// encrypt/decrypt работают одним ключом: шифруем и расшифровываем тем же,
		// которым уже пользуется сервис.
		newKey = oldKey
	default:
		usage("unknown MODE " + mode + " (expected rotate, encrypt or decrypt)")
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
	// Обратный ход касается только app_settings: креды узлов читаются строгим
	// Decrypt во всех трёх сервисах, и открытый текст в них означал бы, что
	// узлы перестали работать (§90.6).
	if !opts.Decrypt {
		nodeStats, err := keyrotate.RotateNodes(ctx, pool, oldC, newC, opts, logger)
		st.Add(nodeStats)
		if err != nil {
			logger.ErrorWithOp("rotate nodes failed", err, "rotate-key.nodes")
			report(logger, st, mode, opts)
			os.Exit(1)
		}
	} else {
		logger.Info("decrypt mode: node credentials are left encrypted on purpose (§90.6)")
	}

	appStats, err := keyrotate.RotateAppSettings(ctx, pool, oldC, newC, opts, logger)
	st.Add(appStats)
	if err != nil {
		logger.ErrorWithOp("rotate app_settings failed", err, "rotate-key.app_settings")
		report(logger, st, mode, opts)
		os.Exit(1)
	}

	report(logger, st, mode, opts)

	if opts.Decrypt && !opts.DryRun && st.Decrypted > 0 {
		logger.Warn("secrets are now stored in PLAINTEXT — this is only for rolling the code back; " +
			"re-run with MODE=encrypt once the rollback is over")
	}
}

func usage(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  OLD_KEY=<base64> NEW_KEY=<base64> [DRY_RUN=true] rotate-key [--config=path]")
	fmt.Fprintln(os.Stderr, "  MODE=encrypt OLD_KEY=<base64> [DRY_RUN=true] rotate-key [--config=path]")
	fmt.Fprintln(os.Stderr, "  MODE=decrypt OLD_KEY=<base64> [DRY_RUN=true] rotate-key [--config=path]")
	os.Exit(2)
}

func report(logger logging.Logger, st keyrotate.Stats, mode string, opts keyrotate.Options) {
	logger.Info("rotate-key finished",
		logger.Str("mode", mode),
		logger.Int("rows_scanned", st.Scanned),
		logger.Int("rows_reencrypted", st.Reencrypted),
		logger.Int("rows_encrypted", st.Encrypted),
		logger.Int("rows_decrypted", st.Decrypted),
		logger.Int("rows_skipped_already_new", st.SkippedAlreadyNew),
		logger.Int("rows_skipped_plaintext", st.SkippedPlaintext),
		logger.Int("rows_empty", st.Empty),
		logger.Any("dry_run", opts.DryRun))
}
