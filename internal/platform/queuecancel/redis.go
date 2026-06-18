// Package queuecancel — Redis-набор «отменённых» async-сообщений (§34.4).
//
// Kafka — append-only лог: физически удалить одно сообщение или произвольный
// период нельзя. Поэтому «удаление» из очереди nexus.async реализовано
// логически: Web-сервис кладёт Envelope.ID в этот набор, а Sender-consumer
// перед отправкой во внешний адрес проверяет набор и пропускает отменённые
// сообщения (commit offset без отправки/DLQ).
//
// Ключи per-ID — qcancel:<id> = "1" с TTL = retention топика. Каждый tombstone
// самоистекает вместе с физическим устареванием сообщения в Kafka — без
// неограниченного роста набора. При недоступности Redis обе операции
// деградируют мягко (fail-open): доставку никогда не блокируем (§9.4).
package queuecancel

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const keyPrefix = "qcancel:"

// Redis — реализация cancel-set поверх go-redis. Пишется Web-сервисом
// (Cancel), читается Sender-сервисом (IsCancelled).
type Redis struct {
	client *goredis.Client
}

// New создаёт cancel-set. client должен быть ненулевым (вызывающая сторона
// не конструирует Redis при отсутствии Redis-клиента).
func New(client *goredis.Client) *Redis {
	return &Redis{client: client}
}

func key(id string) string { return keyPrefix + id }

// Cancel помечает сообщения с указанными ID отменёнными на ttl (retention
// топика). Идемпотентно (SET перезаписывает). Возвращает число обработанных ID.
func (r *Redis) Cancel(ctx context.Context, ids []string, ttl time.Duration) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	pipe := r.client.Pipeline()
	for _, id := range ids {
		if id == "" {
			continue
		}
		pipe.Set(ctx, key(id), "1", ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("queuecancel set: %w", err)
	}
	return len(ids), nil
}

// IsCancelled возвращает true, если сообщение с этим ID было отменено.
// При ошибке Redis возвращает (false, err) — вызывающая сторона (Sender)
// трактует это как fail-open и доставляет сообщение.
func (r *Redis) IsCancelled(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	n, err := r.client.Exists(ctx, key(id)).Result()
	if err != nil {
		return false, fmt.Errorf("queuecancel exists: %w", err)
	}
	return n > 0, nil
}
