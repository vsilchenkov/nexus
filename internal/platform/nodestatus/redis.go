// Package nodestatus — Redis-персист исхода последнего исходящего вызова узла
// (§46, §52). Делает runtime-бейдж узла (§41) устойчивым к рестарту/деплою:
// гаудж nexus_node_last_request_error in-memory и теряется при рестарте
// процесса (после деплоя узлы ложно показывают «OK»), а этот ключ в Redis
// переживает рестарт любого из процессов. Sender пишет (RedisWriter), Web читает.
//
// Ключ nexus:node:last_error:<path>; TTL ~30 суток — ключи удалённых/
// простаивающих узлов самоистекают, без неограниченного роста. Best-effort:
// ошибка Redis логируется и не пробрасывается — статус узла не должен влиять
// на доставку (в духе fail-open §9.4 и §41).
//
// Кодировка значения (§52) — НАМЕРЕННО не совпадает с гауджем
// (domain.NodeOutcome.GaugeValue: 0=ok/1=degraded/2=down):
//
//	значение | outcome  | почему
//	---------+----------+---------------------------------------------------
//	"0"      | ok       | как в булевой схеме §46
//	"1"      | down     | legacy: старый Sender писал "1" = «любой не-2xx» —
//	         |          | новый читатель толкует worst case (down); старый
//	         |          | Web (s == "1") при новом Sender продолжает красить
//	         |          | реальный down красным (rolling-совместимость)
//	"2"      | degraded | новое значение; старый Web покажет OK (транзиентно)
//
// Нераспознанное значение читатель пропускает (fallback на Prometheus, как
// отсутствующий ключ). Матрица rolling-комбинаций — в specs/sections/52.
package nodestatus

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
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

// EncodeOutcome кодирует исход в Redis-значение (таблица — в doc пакета).
func EncodeOutcome(o domain.NodeOutcome) string {
	switch o {
	case domain.NodeOutcomeDown:
		return "1"
	case domain.NodeOutcomeDegraded:
		return "2"
	default:
		return "0"
	}
}

// DecodeOutcome разбирает Redis-значение исхода. Второй результат false —
// значение не распознано (мусор/будущая схема): вызывающая сторона пропускает
// узел, срабатывает fallback на Prometheus. Legacy "1" (булев «любой не-2xx»
// от старого Sender) намеренно читается как down — worst case, самоисцелится
// следующим вызовом узла.
func DecodeOutcome(s string) (domain.NodeOutcome, bool) {
	switch s {
	case "0":
		return domain.NodeOutcomeOK, true
	case "1":
		return domain.NodeOutcomeDown, true
	case "2":
		return domain.NodeOutcomeDegraded, true
	default:
		return "", false
	}
}

// Noop — заглушка Writer для случая «нет Redis»: поведение остаётся как в §41
// (только in-memory гаудж, теряется при рестарте). Используется на call-site
// вместо nil, чтобы не плодить nil-проверки.
type Noop struct{}

// SetLastOutcome ничего не делает.
func (Noop) SetLastOutcome(context.Context, string, domain.NodeOutcome) {}

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

// SetLastOutcome best-effort пишет исход последнего вызова узла в Redis
// (кодировка — EncodeOutcome). Ошибку Redis логирует Warn и не возвращает —
// фиксация статуса не должна влиять на доставку запроса.
func (w *RedisWriter) SetLastOutcome(ctx context.Context, nodePath string, outcome domain.NodeOutcome) {
	if nodePath == "" {
		return
	}
	if err := w.client.Set(ctx, Key(nodePath), EncodeOutcome(outcome), defaultTTL).Err(); err != nil {
		w.logger.Warn("nodestatus: redis set failed",
			w.logger.Str("node", nodePath), w.logger.Err(err))
	}
}
