// Package logging — тонкая обёртка над github.com/vsilchenkov/logging.
//
// Логгер инициализируется ровно один раз в main каждого сервиса и передаётся
// в каждый конструктор через DI. Никаких пакетных глобалов — это требование
// §14.1 ТЗ.
package logging

import (
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
func Init(c *Config, s *SentryConfig) Logger {
	return extlog.Initlogger(c, s)
}

// SentryClientOptions — re-export для main, где явно инициализируется
// sentry.Init (для случаев, когда нужны кастомные опции).
func SentryClientOptions(s *SentryConfig) any {
	return extlog.SentryClientOptions(s)
}
