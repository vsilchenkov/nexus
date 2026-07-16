// Package logging — тонкая обёртка над github.com/vsilchenkov/logging.
//
// Логгер инициализируется ровно один раз в main каждого сервиса и передаётся
// в каждый конструктор через DI. Никаких пакетных глобалов — это требование
// §14.1 ТЗ.
package logging

import (
	"io"
	"log/slog"
	"os"

	extlog "github.com/vsilchenkov/logging"
)

// Logger — алиас на интерфейс из внешнего пакета.
// Используется во всех конструкторах как тип зависимости.
type Logger = extlog.Logger

// Config — конфигурация логгера (re-export для удобства).
type Config = extlog.Config

// SentryConfig — конфигурация Sentry (re-export).
type SentryConfig = extlog.SentryConfig

// Init создаёт логгер. Должен вызываться один раз — в main.
//
// Deprecated: bootstrap.Init собирает цепочку хендлеров сам (§51 — runtime-смена
// уровня через *slog.LevelVar, вендорный Initlogger уровень на лету не меняет).
// Оставлено для совместимости.
func Init(c *Config, s *SentryConfig) Logger {
	return extlog.Initlogger(c, s)
}

// NewLogger оборачивает готовый *slog.Logger в Logger (re-export).
// Используется bootstrap'ом при собственной сборке цепочки хендлеров (§51).
func NewLogger(s *slog.Logger) Logger {
	return extlog.NewLogger(s)
}

// NewMultiHandler — fan-out slog.Handler по нескольким хендлерам (re-export):
// Enabled = OR, Handle доставляет запись каждому включённому хендлеру.
func NewMultiHandler(handlers ...slog.Handler) slog.Handler {
	return extlog.NewMultiHandler(handlers...)
}

// SentryHandler — slog.Handler, шлющий записи >= level в Sentry (re-export).
func SentryHandler(level slog.Level) slog.Handler {
	return extlog.SentryHandler(level)
}

// OutputLogFile открывает (создавая каталог) файл логов workingDir/dir/fileName
// в режиме append (re-export вендорного GetOutputLogFile — parity пути и прав).
func OutputLogFile(workingDir, dir, fileName string) (*os.File, error) {
	return extlog.GetOutputLogFile(workingDir, dir, fileName)
}

// SentryClientOptions — re-export для main, где явно инициализируется
// sentry.Init (для случаев, когда нужны кастомные опции).
func SentryClientOptions(s *SentryConfig) any {
	return extlog.SentryClientOptions(s)
}

// NewNoop возвращает логгер, который сбрасывает весь вывод в io.Discard.
// Используется в unit-тестах, чтобы не зависеть от файловой системы и не
// засорять stderr.
func NewNoop() Logger {
	return extlog.NewLogger(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		Level: slog.LevelError,
	})))
}
