package circuitbreaker

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Snapshot — состояние breaker'а узла для диагностики и UI (§81.4).
//
// Threshold/Cooldown — политика, записанная тем Sender'ом, который вёл счёт
// отказов (см. RecordFailure). Нули означают «неизвестна»: ключ создан версией
// до §81.3 либо breaker ещё ни разу не считал отказы. UI в этом случае обязан
// показать счётчик без знаменателя, а не подставлять своё значение — оно может
// отличаться от применённого (политика переопределяется на узле).
type Snapshot struct {
	Exists     bool          // ключа нет → breaker никогда не открывался
	State      State         // при Exists=false — StateClosed
	Failures   int           // отказов подряд
	Threshold  int           // порог, при котором breaker откроется; 0 — неизвестен
	OpenedAt   time.Time     // момент открытия; нулевое, если состояние не open
	Cooldown   time.Duration // пауза до половинчато-открытой пробы; 0 — неизвестна
	RetryAfter time.Duration // сколько ждать пробы; 0 — проба уже разрешена
	Probe      int64         // выдано пробных запросов в half_open
	TTL        time.Duration // сколько ещё живёт ключ; <0 — TTL не установлен
}

// Admin — операторские операции над состоянием breaker'а: чтение снимка и
// ручной сброс (§81.4).
//
// Отдельный тип от Breaker намеренно. Breaker принимает решения и потому знает
// политику; Admin живёт на читающей стороне (Web), где политики нет и быть не
// должно — ему нужен только клиент Redis. Общими остаются формат ключа и
// кодировка полей, поэтому тип лежит в этом же пакете (как nodestatus.Key и
// nodestatus.DecodeOutcome для §52).
//
// В отличие от Allow/IsOpen, методы Admin НЕ fail-open: соврать оператору
// «закрыт», когда состояние неизвестно, хуже, чем честно вернуть ошибку. Fail-open
// нужен там, где на кону доставка, а здесь на кону решение человека.
type Admin struct {
	client *goredis.Client
}

// NewAdmin создаёт Admin поверх того же Redis, в который пишет Sender.
func NewAdmin(client *goredis.Client) *Admin {
	return &Admin{client: client}
}

// Snapshot читает состояние breaker'а без побочных эффектов: half-open проба не
// расходуется, состояние не меняется.
func (a *Admin) Snapshot(ctx context.Context, key string) (Snapshot, error) {
	k := Key(key)
	pipe := a.client.TxPipeline()
	vals := pipe.HMGet(ctx, k, fState, fFailures, fOpenedAt, fProbe, fThreshold, fCooldown)
	ttl := pipe.PTTL(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("read breaker state: %w", err)
	}
	return decodeSnapshot(vals.Val(), ttl.Val()), nil
}

// Reset принудительно возвращает breaker в closed и отдаёт состояние ДО сброса
// (оно уходит в аудит §81.4.2).
//
// Реализован удалением ключа, а не записью closed: отсутствие ключа и есть
// «closed без истории» (см. Allow), поэтому не нужно думать ни о TTL, ни о
// счётчике пробных, ни об устаревшей политике. Идемпотентен: отсутствие ключа —
// не ошибка, в снимке это видно по Exists=false.
//
// Гонка с половинчато-открытой пробой безопасна: проба, упавшая уже после
// сброса, создаст ключ заново с failures=1, то есть узел получит полный бюджет
// попыток — ровно то, чего оператор и добивался.
func (a *Admin) Reset(ctx context.Context, key string) (Snapshot, error) {
	k := Key(key)
	pipe := a.client.TxPipeline()
	vals := pipe.HMGet(ctx, k, fState, fFailures, fOpenedAt, fProbe, fThreshold, fCooldown)
	ttl := pipe.PTTL(ctx, k)
	pipe.Del(ctx, k)
	if _, err := pipe.Exec(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("reset breaker: %w", err)
	}
	return decodeSnapshot(vals.Val(), ttl.Val()), nil
}

// decodeSnapshot собирает Snapshot из ответа HMGET (порядок полей — как в
// запросе) и PTTL. Отсутствующие поля дают нулевые значения: Snapshot обязан
// оставаться читаемым и для ключей, созданных версией до §81.3.
func decodeSnapshot(vals []any, ttl time.Duration) Snapshot {
	str := func(i int) string {
		if i >= len(vals) {
			return ""
		}
		s, _ := vals[i].(string)
		return s
	}
	num := func(i int) int64 {
		n, _ := strconv.ParseInt(str(i), 10, 64)
		return n
	}

	s := Snapshot{
		State:     StateClosed,
		Failures:  int(num(1)),
		Probe:     num(3),
		Threshold: int(num(4)),
		Cooldown:  time.Duration(num(5)),
		TTL:       ttl,
	}
	if v := str(0); v != "" {
		s.Exists = true
		s.State = State(v)
	}
	if s.State != StateOpen {
		return s
	}
	if openedNs := num(2); openedNs > 0 {
		s.OpenedAt = time.Unix(0, openedNs)
		if left := s.Cooldown - time.Since(s.OpenedAt); left > 0 {
			s.RetryAfter = left
		}
	}
	return s
}
