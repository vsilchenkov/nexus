// Package nodestatus — Redis-персист исхода последнего исходящего вызова узла
// (§46). Делает индикатор «Down/OK» (§41) устойчивым к рестарту/деплою: гаудж
// nexus_node_last_request_error in-memory и теряется при рестарте процесса
// (после деплоя узлы ложно показывают «OK»), а этот ключ в Redis переживает
// рестарт любого из процессов. Sender пишет (RedisWriter), Web читает.
//
// Ключ nexus:node:last_error:<path> = "1" (последний вызов — ошибка, status
// 0/4xx/5xx) | "0" (успех, 2xx); TTL ~30 суток — ключи удалённых/простаивающих
// узлов самоистекают, без неограниченного роста. Best-effort: ошибка Redis
// логируется и не пробрасывается — статус узла не должен влиять на доставку
// (в духе fail-open §9.4 и §41).
package nodestatus

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/logging"
)

const (
	keyPrefix = "nexus:node:last_error:"
	// defaultTTL — срок жизни ключа исхода. Перезаписывается каждым вызовом узла;
	// длинный TTL нужен лишь чтобы простаивающие/удалённые узлы со временем ушли.
	defaultTTL = 30 * 24 * time.Hour
)

// Key возвращает Redis-ключ исхода последнего вызова узла по его пути. Экспортно,
// чтобы читающая сторона (Web-адаптер) использовала тот же формат без дублирования.
func Key(nodePath string) string { return keyPrefix + nodePath }

// Noop — заглушка Writer для случая «нет Redis»: поведение остаётся как в §41
// (только in-memory гаудж, теряется при рестарте). Используется на call-site
// вместо nil, чтобы не плодить nil-проверки.
type Noop struct{}

// SetLastError ничего не делает.
func (Noop) SetLastError(context.Context, string, bool) {}

// RedisWriter — Redis-реализация записи исхода последнего вызова (пишется Sender'ом).
type RedisWriter struct {
	client *goredis.Client
	logger logging.Logger
}

// NewRedisWriter создаёт writer. client должен быть ненулевым: вызывающая сторона
// не конструирует RedisWriter при отсутствии Redis-клиента (использует Noop).
func NewRedisWriter(client *goredis.Client, logger logging.Logger) *RedisWriter {
	return &RedisWriter{client: client, logger: logger}
}

// SetLastError best-effort пишет исход последнего вызова узла в Redis: errored=true
// → "1" (status 0/4xx/5xx), false → "0" (2xx). Ошибку Redis логирует Warn и не
// возвращает — фиксация статуса не должна влиять на доставку запроса.
func (w *RedisWriter) SetLastError(ctx context.Context, nodePath string, errored bool) {
	if nodePath == "" {
		return
	}
	v := "0"
	if errored {
		v = "1"
	}
	if err := w.client.Set(ctx, Key(nodePath), v, defaultTTL).Err(); err != nil {
		w.logger.Warn("nodestatus: redis set failed",
			w.logger.Str("node", nodePath), w.logger.Err(err))
	}
}
