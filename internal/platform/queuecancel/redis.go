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
// Растёт только по Bump. Планировщик прохода очереди паузы в Sender сравнивает
// значение со своим последним и, если оно изменилось, запускает раунд —
// отменённые сообщения снимаются с очереди за один интервал, а не ждут
// страховочного прохода.
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
// Сигнал прохода очереди паузы Cancel НЕ двигает: это делает вызывающая сторона
// через Bump — после отметок и один раз на операцию (ревизия §113). До неё
// каждая пачка двигала сигнал, и «Повторить все» на N записей (отмена по одной)
// или очистка постороннего узла сбрасывали раунд на каждом тике — то есть
// возвращали перезапись очереди паузы в темпе до §113.
func (r *Redis) Cancel(ctx context.Context, ids []string, ttl time.Duration) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	_, err := r.client.Pipelined(ctx, func(pipe goredis.Pipeliner) error {
		for _, id := range ids {
			if id == "" {
				continue
			}
			pipe.Set(ctx, key(id), "1", ttl)
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

// Bump увеличивает счётчик-сигнал и возвращает новое значение: реплики Sender
// начнут раунд прохода очереди паузы (§113.3). Зовут его Sender (старт процесса и
// страховочный проход — сама реплика запоминает значение и не принимает
// собственный инкремент за чужой сигнал) и Web — после отметок отмены у узла на
// паузе. Вызов после Cancel упорядочен: увидевший сигнал видит и отметки.
func (r *Redis) Bump(ctx context.Context) (int64, error) {
	v, err := r.client.Incr(ctx, signalKey).Result()
	if err != nil {
		return 0, fmt.Errorf("queuecancel bump: %w", err)
	}
	return v, nil
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
