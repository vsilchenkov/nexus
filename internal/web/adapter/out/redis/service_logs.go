package redis

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/logsink"
	"nexus/internal/web/usecase/port"
)

// ServiceLogReaderRedis — реализация port.ServiceLogReader поверх Redis-колец
// служебных логов (§51.5): LRANGE nexus:logs:<service> (LPUSH-порядок — индекс
// 0 = самая свежая запись).
type ServiceLogReaderRedis struct {
	rdb    *goredis.Client
	logger logging.Logger
}

var _ port.ServiceLogReader = (*ServiceLogReaderRedis)(nil)

// NewServiceLogReaderRedis создаёт ридер поверх готового клиента Redis.
func NewServiceLogReaderRedis(rdb *goredis.Client, logger logging.Logger) *ServiceLogReaderRedis {
	return &ServiceLogReaderRedis{rdb: rdb, logger: logger}
}

// Tail возвращает до limit свежих записей сервиса (новые первыми).
func (r *ServiceLogReaderRedis) Tail(ctx context.Context, service string, limit int) ([]domain.ServiceLogEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	lines, err := r.rdb.LRange(ctx, logsink.Key(service), 0, int64(limit-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("lrange %s: %w", logsink.Key(service), err)
	}
	return parseEntries(lines, service, r.logger), nil
}

// parseEntries разбирает JSON-строки кольца в записи. Битая строка — skip с
// debug-логом, НЕ ошибка: одна повреждённая запись не должна валить консоль.
func parseEntries(lines []string, service string, logger logging.Logger) []domain.ServiceLogEntry {
	out := make([]domain.ServiceLogEntry, 0, len(lines))
	for _, line := range lines {
		var e domain.ServiceLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if logger != nil {
				logger.Debug("service log line skipped: malformed json",
					logger.Str("service", service), logger.Err(err))
			}
			continue
		}
		if e.Service == "" {
			e.Service = service
		}
		out = append(out, e)
	}
	return out
}
