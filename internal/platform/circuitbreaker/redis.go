// Package circuitbreaker — Redis-backed CB (§9.5 ТЗ).
//
// Состояние per-key (обычно key = node_path) с тремя положениями:
//
//	closed    — обычная работа;
//	open      — после consecutive failures >= threshold; запросы
//	            отбрасываются на cooldown секунд;
//	half_open — после cooldown ровно ОДИН пробный запрос; success → closed,
//	            failure → opens обратно. Остальные запросы в half_open
//	            отбрасываются, пока судьба пробного не решена.
//
// Реализация — Redis-hash per key: failures (int) + state (string)
// + opened_at (unix nanoseconds — нужны сабсекундные cooldown'ы, например
// в integration-тестах) + probe (счётчик пробных в half_open).
//
// Переходы open→half_open и выдача пробного делаются Lua-скриптом —
// атомарно относительно конкурентных Allow: без этого N запросов,
// одновременно заставших истёкший cooldown, прошли бы «пробными» все разом.
//
// Если процесс упал, не записав результат пробного (RecordSuccess/Failure),
// ключ самоочищается TTL'ом 10 минут — breaker вернётся в closed.
package circuitbreaker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

// keyPrefix/keyTTL — формат ключа и его время жизни.
const (
	keyPrefix = "circuit:"
	keyTTL    = 10 * time.Minute
)

// Поля hash'а. Неэкспортные: кодировка — внутреннее дело пакета, наружу она
// выходит только типизированным Snapshot (см. admin.go).
const (
	fState     = "state"
	fFailures  = "failures"
	fOpenedAt  = "opened_at" // unix nanoseconds
	fProbe     = "probe"     // счётчик пробных запросов в half_open
	fThreshold = "threshold" // §81.3: политика, применённая при открытии
	fCooldown  = "cooldown_ns"
)

// Key возвращает Redis-ключ состояния breaker'а узла (§81.1).
//
// Экспортно по той же причине, что и nodestatus.Key: состояние читает и
// сбрасывает ДРУГОЙ процесс (Web), и формат ключа обязан быть один на всех, а
// не продублирован литералом в каждом пакете.
func Key(nodeKey string) string { return keyPrefix + nodeKey }

// allowProbeScript — атомарная часть Allow для состояний open/half_open.
// KEYS[1] — hash ключа; ARGV[1] — now (unix ns); ARGV[2] — cooldown (ns).
// Возвращает 1 (пропустить запрос) или 0 (отбросить).
//
// goredis.Script неизменяем после создания (хранит только текст и SHA) —
// package-level var здесь эквивалентен константе.
var allowProbeScript = goredis.NewScript(`
local state = redis.call("HGET", KEYS[1], "state")
if state == "open" then
	local opened = tonumber(redis.call("HGET", KEYS[1], "opened_at") or "0")
	if tonumber(ARGV[1]) - opened >= tonumber(ARGV[2]) then
		redis.call("HSET", KEYS[1], "state", "half_open", "probe", 1)
		return 1
	end
	return 0
elseif state == "half_open" then
	if redis.call("HINCRBY", KEYS[1], "probe", 1) == 1 then
		return 1
	end
	return 0
end
return 1
`)

type Breaker struct {
	client    *goredis.Client
	threshold int
	cooldown  time.Duration
}

func New(client *goredis.Client, threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{client: client, threshold: threshold, cooldown: cooldown}
}

// Allow возвращает true, если breaker разрешает запрос.
// open + cooldown ещё не истёк → false; cooldown истёк → ровно один
// запрос проходит пробным (half_open), остальные ждут его результата.
func (b *Breaker) Allow(ctx context.Context, key string) (bool, error) {
	k := Key(key)
	state, err := b.client.HGet(ctx, k, fState).Result()
	if err != nil {
		if err == goredis.Nil {
			return true, nil
		}
		// fail-open: считаем closed (§9.4 ТЗ)
		return true, err
	}
	if State(state) == "" || State(state) == StateClosed {
		return true, nil
	}
	res, err := allowProbeScript.Run(ctx, b.client, []string{k},
		time.Now().UnixNano(), b.cooldown.Nanoseconds()).Int()
	if err != nil {
		// fail-open: считаем closed (§9.4 ТЗ)
		return true, err
	}
	return res == 1, nil
}

// RecordSuccess сбрасывает счётчик ошибок и устанавливает closed.
func (b *Breaker) RecordSuccess(ctx context.Context, key string) error {
	k := Key(key)
	pipe := b.client.TxPipeline()
	pipe.HSet(ctx, k, fState, string(StateClosed), fFailures, 0)
	pipe.HDel(ctx, k, fOpenedAt, fProbe)
	pipe.Expire(ctx, k, keyTTL) // авто-чистка
	_, err := pipe.Exec(ctx)
	return err
}

// RecordFailure инкрементирует счётчик, при достижении порога — open.
//
// Вместе со счётчиком в hash пишется применённая политика (threshold,
// cooldown) — §81.3. Иначе UI не сможет показать «3 из 5» и обратный отсчёт до
// пробы: поля Breaker'а неэкспортны, читающая сторона живёт в другом процессе,
// а после переопределения политики на узле глобальная конфигурация перестаёт
// быть источником истины. Цена нулевая: путь редкий, поля дописываются в уже
// существующий pipeline.
func (b *Breaker) RecordFailure(ctx context.Context, key string) error {
	k := Key(key)
	failures, err := b.client.HIncrBy(ctx, k, fFailures, 1).Result()
	if err != nil {
		return err
	}
	pipe := b.client.TxPipeline()
	pipe.HSet(ctx, k, fThreshold, b.threshold, fCooldown, b.cooldown.Nanoseconds())
	if int(failures) >= b.threshold {
		pipe.HSet(ctx, k, fState, string(StateOpen),
			fOpenedAt, strconv.FormatInt(time.Now().UnixNano(), 10))
		pipe.HDel(ctx, k, fProbe)
	}
	pipe.Expire(ctx, k, keyTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// IsOpen — read-only проверка: breaker открыт И cooldown ещё НЕ истёк
// (адрес считается мёртвым прямо сейчас). Открытый breaker с истёкшим cooldown
// → false: пусть следующий Allow выдаст half-open-пробу. half_open и closed →
// false. В отличие от Allow без побочных эффектов — не расходует half-open-пробу
// и не меняет состояние. Используется DLQ-репроцессором (§36.3 шаг5), чтобы не
// плодить fast-fail-churn, пока адрес мёртв.
func (b *Breaker) IsOpen(ctx context.Context, key string) (bool, error) {
	k := Key(key)
	vals, err := b.client.HMGet(ctx, k, fState, fOpenedAt).Result()
	if err != nil {
		// fail-open: при ошибке Redis не считаем breaker открытым (§9.4).
		return false, fmt.Errorf("hmget: %w", err)
	}
	state, _ := vals[0].(string)
	if State(state) != StateOpen {
		return false, nil
	}
	openedStr, _ := vals[1].(string)
	opened, _ := strconv.ParseInt(openedStr, 10, 64)
	return time.Now().UnixNano()-opened < b.cooldown.Nanoseconds(), nil
}

// State возвращает текущее состояние (для диагностики/UI).
func (b *Breaker) State(ctx context.Context, key string) (State, error) {
	v, err := b.client.HGet(ctx, Key(key), fState).Result()
	if err != nil {
		if err == goredis.Nil {
			return StateClosed, nil
		}
		return "", fmt.Errorf("hget: %w", err)
	}
	if v == "" {
		return StateClosed, nil
	}
	return State(v), nil
}
