// Package runner — единая точка запуска бинаря под обоими режимами:
//   - docker / интерактивный foreground — реагирует на SIGTERM/SIGINT;
//   - Windows-сервис под SCM (kardianos/service) — реагирует на Service Control.
//
// Каждый main вызывает runner.Run(serviceName, app); внутри обёртки выбор
// делается через service.Interactive().
package runner

import (
	"context"
	"fmt"

	"github.com/kardianos/service"

	"bus/internal/platform/logging"
)

// App — контракт приложения, которое умеет стартовать и останавливаться.
// Start должен быть блокирующим до Stop или фатальной ошибки.
type App interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Run оборачивает приложение в kardianos/service и запускает.
// Возвращается только после Stop.
func Run(serviceName, displayName, description string, app App, logger logging.Logger) error {
	cfg := &service.Config{
		Name:        serviceName,
		DisplayName: displayName,
		Description: description,
	}

	prg := &program{app: app, logger: logger}

	s, err := service.New(prg, cfg)
	if err != nil {
		return fmt.Errorf("service.New: %w", err)
	}

	prg.svc = s

	if err := s.Run(); err != nil {
		return fmt.Errorf("service.Run: %w", err)
	}
	return nil
}

// program реализует service.Interface для kardianos/service.
type program struct {
	app    App
	logger logging.Logger
	svc    service.Service

	ctx    context.Context
	cancel context.CancelFunc
}

// Start вызывается SCM — должен быстро вернуться. Запускаем app в горутине.
func (p *program) Start(_ service.Service) error {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	go func() {
		if err := p.app.Start(p.ctx); err != nil {
			p.logger.ErrorWithOp("app start failed", err, "runner.Start")
			// В режиме интерактивного запуска просим обёртку остановиться,
			// чтобы s.Run() вернул управление в main.
			if service.Interactive() && p.svc != nil {
				_ = p.svc.Stop()
			}
		}
	}()
	return nil
}

// Stop вызывается SCM или сигналом. Должен завершить app graceful.
func (p *program) Stop(_ service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	return p.app.Stop(context.Background())
}
