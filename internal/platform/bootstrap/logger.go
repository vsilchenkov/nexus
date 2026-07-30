package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-colorable"
	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/logging"
	"nexus/internal/platform/logsink"
	"nexus/internal/platform/safego"
)

// Сборка цепочки slog-хендлеров (§51 ТЗ). Вендорный logging.Initlogger
// фиксирует уровень на старте — здесь та же цепочка (tint-stderr ИЛИ JSON-файл,
// + Sentry fan-out) собирается из экспортируемых примитивов вендора, но с
// *slog.LevelVar: уровень базового вывода и кольца логов меняется в runtime.
// Порог Sentry-хендлера остаётся собственным (sentry.level) и от LevelVar
// не зависит — как у вендора.

// intLevel мапит конфиг-уровень (2..5) в slog.Level — зеркало levelMap вендора.
// ok=false для значения вне диапазона (уровень по умолчанию — Info).
func intLevel(l int) (slog.Level, bool) {
	switch l {
	case 5:
		return slog.LevelDebug, true
	case 4:
		return slog.LevelInfo, true
	case 3:
		return slog.LevelWarn, true
	case 2:
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

// slogLevelFromInt — intLevel с дефолтом Info (поведение вендора).
func slogLevelFromInt(l int) slog.Level {
	lvl, _ := intLevel(l)
	return lvl
}

// buildBaseHandler повторяет базовый хендлер вендорного Initlogger:
// !OutputInFile → цветной tint (время "15:04:05"); иначе JSON в файл app.log
// в WorkingDir/Dir (fallback — JSON в console при ошибке открытия файла).
// console параметром — для тестов (в проде os.Stderr); lv — общий LevelVar.
// Возвращаемый closer не-nil только для файлового вывода: в проде файл живёт
// до конца процесса (как у вендора), тестам нужен close для cleanup на Windows.
func buildBaseHandler(c *logging.Config, lv slog.Leveler, console io.Writer) (slog.Handler, io.Closer) {
	if !c.OutputInFile {
		w := console
		if f, ok := console.(*os.File); ok {
			w = colorable.NewColorable(f)
		}
		return tint.NewHandler(w, &tint.Options{
			Level:      lv,
			TimeFormat: "15:04:05",
			AddSource:  false,
			NoColor:    false,
		}), nil
	}
	const fileName = "app.log"
	file, err := logging.OutputLogFile(c.WorkingDir, c.Dir, fileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log file %q, falling back to stderr: %v\n", fileName, err)
		return slog.NewJSONHandler(console, &slog.HandlerOptions{
			Level:     lv,
			AddSource: false,
		}), nil
	}
	return slog.NewJSONHandler(file, &slog.HandlerOptions{
		Level:     lv,
		AddSource: false,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.String(a.Key, a.Value.Time().Format("2006-01-02 15:04:05"))
			}
			return a
		},
	}), file
}

// assembleChain собирает итоговый хендлер: base + ring (+ sentry при не-nil)
// через fan-out MultiHandler вендора.
func assembleChain(base, ring, sentry slog.Handler) slog.Handler {
	handlers := []slog.Handler{base}
	if ring != nil {
		handlers = append(handlers, ring)
	}
	if sentry != nil {
		handlers = append(handlers, sentry)
	}
	if len(handlers) == 1 {
		return base
	}
	return logging.NewMultiHandler(handlers...)
}

// LogController — ручка управления логированием процесса (§51): Level меняет
// порог base+ring хендлеров в runtime, Ring отдаёт записи шипперу/снапшоту.
type LogController struct {
	Level *slog.LevelVar
	Ring  *logsink.RingHandler

	// fallbackLevel — YAML-уровень (2..5) на случай сброса app_settings.
	fallbackLevel int
	shipperOnce   sync.Once
}

// FallbackLevel — уровень из YAML-конфига (2..5), к которому откатываемся,
// когда app_settings.logging.level не задан.
func (c *LogController) FallbackLevel() int { return c.fallbackLevel }

// NewLogController собирает контроллер с собственными LevelVar и кольцом —
// для integration-тестов, где полноценный Init (флаги/конфиг/Sentry) не нужен.
// fallbackLevel — YAML-шкала 2..5.
func NewLogController(fallbackLevel int, service string) *LogController {
	lv := new(slog.LevelVar)
	lv.Set(slogLevelFromInt(fallbackLevel))
	return &LogController{
		Level:         lv,
		Ring:          logsink.NewRingHandler(lv, service),
		fallbackLevel: fallbackLevel,
	}
}

// StartRedisShipper запускает фоновый шиппер кольца в Redis (nexus:logs:<svc>).
// Nil-safe и идемпотентен (первый вызов выигрывает). Возвращает done-канал
// горутины шиппера — Stop сервиса дожидается его (Phase AUD.3); при no-op
// возвращается уже закрытый канал.
func (c *LogController) StartRedisShipper(ctx context.Context, rdb *goredis.Client, logger logging.Logger) <-chan struct{} {
	var done <-chan struct{}
	if c != nil && c.Ring != nil && rdb != nil {
		c.shipperOnce.Do(func() {
			sh := logsink.NewShipper(logsink.NewRedisWriter(rdb), c.Ring.Entries(), c.Ring.Service())
			done = safego.Go(logger, "logsink.shipper", func() {
				sh.Run(ctx)
			})
		})
	}
	if done == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return done
}

// buildLogger собирает логгер сервиса с runtime-уровнем: LevelVar из
// cfg.Logging.Level, базовый вывод (parity с вендором), кольцо логов и
// Sentry fan-out при sentryCfg.Use (порог — sentry.level, fallback — базовый).
func buildLogger(logCfg *logging.Config, sentryCfg *logging.SentryConfig, service, instanceID string) (logging.Logger, *LogController) {
	lv := new(slog.LevelVar)
	baseLevel := slogLevelFromInt(logCfg.Level)
	lv.Set(baseLevel)

	// Файл логов (если включён) живёт до конца процесса — closer не нужен.
	base, _ := buildBaseHandler(logCfg, lv, os.Stderr)
	// §70.7: каждая запись кольца несёт идентификатор ноды — иначе в консоли
	// логов записи двух нод неотличимы.
	ring := logsink.NewRingHandler(lv, service, logsink.WithInstance(instanceID))

	var sentryH slog.Handler
	if sentryCfg.Use {
		sLvl, ok := intLevel(sentryCfg.Level)
		if !ok {
			sLvl = baseLevel
		}
		sentryH = logging.SentryHandler(sLvl)
	}

	logger := logging.NewLogger(slog.New(assembleChain(base, ring, sentryH)))
	return logger, &LogController{Level: lv, Ring: ring, fallbackLevel: logCfg.Level}
}
