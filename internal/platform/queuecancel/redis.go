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
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const keyPrefix = "qcancel:"

// signalKey — счётчик-сигнал «в очереди паузы что-то отменили» (§113.3).
// Растёт вместе с каждой пачкой отметок отмены (той же транзакцией) и по Bump.
// Планировщик прохода очереди паузы в Sender сравнивает значение со своим
// последним и, если оно изменилось, запускает раунд — отменённые сообщения
// снимаются с очереди за один интервал, а не ждут страховочного прохода.
const signalKey = "nexus:paused-sweep:signal"

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
//
// Отметки и инкремент сигнала (§113.3) пишутся одной транзакцией MULTI/EXEC:
// планировщик, увидевший новый сигнал, гарантированно видит и все отметки —
// иначе раунд мог бы пройти раньше, чем отметки появились, и следующего сигнала
// уже не было бы.
func (r *Redis) Cancel(ctx context.Context, ids []string, ttl time.Duration) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	_, err := r.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		marked := 0
		for _, id := range ids {
			if id == "" {
				continue
			}
			pipe.Set(ctx, key(id), "1", ttl)
			marked++
		}
		if marked > 0 {
			pipe.Incr(ctx, signalKey)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("queuecancel set: %w", err)
	}
	return len(ids), nil
}

// Signal возвращает текущее значение счётчика-сигнала (§113.3); ключа нет — 0.
func (r *Redis) Signal(ctx context.Context) (int64, error) {
	v, err := r.client.Get(ctx, signalKey).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("queuecancel signal: %w", err)
	}
	return v, nil
}

// Bump увеличивает счётчик-сигнал: все реплики Sender начнут раунд прохода
// очереди паузы (§113.3 — старт процесса и страховочный проход).
func (r *Redis) Bump(ctx context.Context) error {
	if err := r.client.Incr(ctx, signalKey).Err(); err != nil {
		return fmt.Errorf("queuecancel bump: %w", err)
	}
	return nil
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
