package usecase

import (
	"context"
	"time"

	"nexus/internal/platform/safego"
)

// KafkaBrokerBudget — сколько экран Kafka ждёт брокер на одно чтение (§114.4).
// Не уложилось — значение «не прочитано», ответ не ждёт таймаута клиента
// kafka-go (5 с на каждый запрос, а запросов в чтении несколько).
type KafkaBrokerBudget struct {
	// Overview — lag группы или состояние брокеров для /api/kafka/overview.
	Overview time.Duration
	// Topics — обход всех топиков и групп кластера для /api/kafka/topics.
	Topics time.Duration
}

// DefaultKafkaBrokerBudget — бюджеты по умолчанию (§114.4): 3 с на чтение
// overview, 10 с на обход топиков.
func DefaultKafkaBrokerBudget() KafkaBrokerBudget {
	return KafkaBrokerBudget{Overview: 3 * time.Second, Topics: 10 * time.Second}
}

// WithKafkaBrokerBudget задаёт бюджеты чтения брокера; поле ≤ 0 оставляет
// значение по умолчанию. Меньшие бюджеты задают тесты.
func WithKafkaBrokerBudget(b KafkaBrokerBudget) KafkaMonitorOption {
	return func(u *KafkaMonitorUsecase) {
		if b.Overview > 0 {
			u.budget.Overview = b.Overview
		}
		if b.Topics > 0 {
			u.budget.Topics = b.Topics
		}
	}
}

// Ключи кеша чтений брокера (§114.4). Префикс v2: в значении лежит
// brokerRead{value, ok}, а не голое значение, как до §114, — старые ключи
// другой формы истекают сами, а смешанный выкат реплик Web не читает чужую
// форму.
const (
	brokerKeyTopics  = "v2:topics"
	brokerKeyBrokers = "v2:brokers"
	brokerKeyLag     = "v2:lag:"
)

// brokerRead — итог чтения брокера в кеше. Неудача (OK=false) кешируется
// наравне с успехом: при мёртвом брокере бюджет платит только первый запрос
// окна TTL, а не каждое автообновление каждой вкладки (§114.4).
type brokerRead[T any] struct {
	Value T    `json:"value"`
	OK    bool `json:"ok"`
}

// readBroker — чтение брокера с кешем, singleflight и бюджетом (§114.4).
// ok=false — не прочитано: брокер не уложился в бюджет или ответил ошибкой
// (сейчас или в пределах TTL кеша).
//
// Одновременные запросы реплики ждут одно чтение (singleflight по ключу), а
// само чтение отвязано от отмены запроса: закрытая вкладка первого пришедшего
// не обрывает его остальным и не кладёт в кеш ложную неудачу. Ограничивает
// чтение только бюджет. Значение T общее для всех ждущих — изменяемые
// значения (срезы) вызывающий клонирует.
func readBroker[T any](ctx context.Context, u *KafkaMonitorUsecase, key string, budget time.Duration, fetch func(context.Context) (T, error)) (T, bool) {
	if u.cache != nil {
		var cached brokerRead[T]
		if found, err := u.cache.Get(ctx, key, &cached); found && err == nil {
			if !cached.OK {
				// §51.9: неудача из кеша — брокер в этот раз не спрашивали.
				u.logger.Debug("kafka broker read: cached failure, broker not asked",
					u.logger.Str("op", "web.kafkaMonitor.brokerRead"), u.logger.Str("key", key))
			}
			return cached.Value, cached.OK
		}
	}
	detached := context.WithoutCancel(ctx)
	ch := u.flight.DoChan(key, func() (any, error) {
		return fetchBroker(detached, u, key, budget, fetch), nil
	})
	select {
	case res := <-ch:
		if r, ok := res.Val.(brokerRead[T]); ok {
			return r.Value, r.OK
		}
		// Недостижимо: под одним ключом всегда один тип T. Паника в чтении
		// сюда не ведёт — fetchBroker гасит её и возвращает OK=false.
		var zero T
		return zero, false
	case <-ctx.Done():
		u.logger.Debug("kafka broker read: request gone before broker answered",
			u.logger.Str("op", "web.kafkaMonitor.brokerRead"), u.logger.Str("key", key))
		var zero T
		return zero, false
	}
}

// fetchBroker выполняет чтение в пределах бюджета и кладёт итог в кеш —
// удачный или нет. Выполняется в горутине singleflight.
func fetchBroker[T any](ctx context.Context, u *KafkaMonitorUsecase, key string, budget time.Duration, fetch func(context.Context) (T, error)) brokerRead[T] {
	defer safego.RecoverCtx(ctx, u.logger, "web.kafkaMonitor.brokerRead")
	rctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	start := time.Now()
	v, err := fetch(rctx)
	r := brokerRead[T]{Value: v, OK: err == nil}
	op, k := u.logger.Str("op", "web.kafkaMonitor.brokerRead"), u.logger.Str("key", key)
	took := u.logger.Int("duration_ms", int(time.Since(start).Milliseconds()))
	if err != nil {
		u.logger.Warn("kafka broker read failed; cached as unavailable",
			op, k, took, u.logger.Str("budget", budget.String()), u.logger.Err(err))
	} else {
		u.logger.Debug("kafka broker read", op, k, took)
	}
	if u.cache != nil {
		// Best-effort: без кеша следующий запрос снова сходит в брокер.
		_ = u.cache.Set(ctx, key, r)
	}
	return r
}
