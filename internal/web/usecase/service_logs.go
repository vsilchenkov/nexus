package usecase

import (
	"context"
	"slices"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/web/usecase/port"
)

// Лимиты консоли служебных логов (§51.5).
const (
	ServiceLogsDefaultLimit = 500
	ServiceLogsMaxLimit     = 2000
)

// ServiceLogsUsecase — вьювер служебных логов (§51.5): читает хвосты Redis-колец
// нужных сервисов, мержит по времени (свежие первыми) и фильтрует по уровню.
type ServiceLogsUsecase struct {
	reader port.ServiceLogReader
	logger logging.Logger
}

// NewServiceLogsUsecase создаёт вьювер поверх ридера колец.
func NewServiceLogsUsecase(reader port.ServiceLogReader, logger logging.Logger) *ServiceLogsUsecase {
	return &ServiceLogsUsecase{reader: reader, logger: logger}
}

// Tail возвращает до limit свежих записей выбранных сервисов, слитых по TS
// (desc). services пуст или содержит "all" → все три сервиса. minLevel ""
// → без фильтра. Ошибка чтения одного сервиса не валит консоль — partial
// результат с warn (упавший Redis-ключ не должен прятать логи остальных).
func (u *ServiceLogsUsecase) Tail(ctx context.Context, services []string, limit int, minLevel string) ([]domain.ServiceLogEntry, error) {
	resolved, err := resolveServices(services)
	if err != nil {
		return nil, err
	}
	minRank := -1
	if minLevel != "" {
		r, ok := domain.ServiceLogLevelRank(minLevel)
		if !ok {
			return nil, domain.ErrServiceLogInvalidLevel
		}
		minRank = r
	}
	switch {
	case limit <= 0:
		limit = ServiceLogsDefaultLimit
	case limit > ServiceLogsMaxLimit:
		limit = ServiceLogsMaxLimit
	}

	merged := make([]domain.ServiceLogEntry, 0, limit*len(resolved))
	for _, svc := range resolved {
		entries, err := u.reader.Tail(ctx, svc, limit)
		if err != nil {
			u.logger.Warn("service logs read failed; returning partial result",
				u.logger.Str("service", svc), u.logger.Err(err))
			continue
		}
		for _, e := range entries {
			if minRank >= 0 {
				// Неизвестный уровень (например slog-офсет) не прячем.
				if r, ok := domain.ServiceLogLevelRank(e.Level); ok && r < minRank {
					continue
				}
			}
			merged = append(merged, e)
		}
	}

	slices.SortStableFunc(merged, func(a, b domain.ServiceLogEntry) int {
		return b.TS.Compare(a.TS) // desc: свежие первыми
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged, nil
}

// resolveServices нормализует параметр service: пусто/"all" → все три;
// иначе — валидация каждого имени с дедупликацией (порядок сохраняется).
func resolveServices(services []string) ([]string, error) {
	if len(services) == 0 {
		return domain.ServiceLogServices(), nil
	}
	out := make([]string, 0, len(services))
	for _, s := range services {
		if s == domain.ServiceLogAll {
			return domain.ServiceLogServices(), nil
		}
		if !domain.ValidServiceLogService(s) {
			return nil, domain.ErrServiceLogInvalidService
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, nil
}
