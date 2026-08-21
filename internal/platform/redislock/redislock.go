// Package redislock — распределённые локи для развёртывания с несколькими
// репликами (§93).
//
// Два примитива, и они про разное:
//
//   - [Lock] (§93.7) — «этот цикл периодической задачи выполняет ровно одна
//     реплика». Без владельца, без продления, снимается только по TTL.
//   - [Lease] (§93.8) — «этот ресурс ведёт вот эта реплика»: с владельцем,
//     продлением и явной отдачей при остановке.
//
// Зачем: с двумя репликами сервиса каждый его тикер срабатывает дважды. Часть
// задач от этого лишь тратит ресурсы (два одинаковых DELETE по одному условию),
// часть ведёт себя заметно хуже — уборка партиций ClickHouse выполняет
// DROP PARTITION дважды, и вторая попытка шумит ошибками в логах ровно там, где
// оператор потом будет искать настоящую причину.
//
// Устройство намеренно простое — SET NX с TTL, как у планировщика уведомлений
// (§20.4, web/adapter/out/redis/notif_lock.go), который эту схему уже пережил в
// бою. Лок НЕ освобождается вручную: истечение по TTL исключает удаление чужого
// лока после паузы GC или долгого цикла. Цена — задача не запустится повторно,
// пока TTL не истёк, поэтому TTL берут заведомо меньше периода задачи.
//
// Чего этот лок НЕ даёт (и не должен): взаимного исключения с гарантией
// корректности. При разрыве сети реплика может продолжать работу с уже
// истёкшим локом, и вторая реплика возьмёт его же. Для задач вида «прибраться
// по расписанию» это допустимо: они идемпотентны, и защищаемся мы от лишней
// работы, а не от порчи данных. Если появится задача, для которой двойное
// выполнение опасно, ей нужен не этот лок, а транзакция в PostgreSQL.
package redislock

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// KeyPrefix — общий префикс ключей локов, чтобы их было видно одним SCAN.
const KeyPrefix = "nexus:lock:"

// Lock — именованный лок периодической задачи.
type Lock struct {
	client *goredis.Client
	key    string
}

// New создаёт лок с ключом nexus:lock:<name>.
func New(client *goredis.Client, name string) *Lock {
	return &Lock{client: client, key: KeyPrefix + name}
}

// Key — полный ключ лока в Redis. Нужен диагностике и тестам.
func (l *Lock) Key() string { return l.key }

// TryLock пытается захватить лок на ttl.
//
// true  — эта реплика выполняет цикл;
// false — лок уже держит другая реплика, цикл надо пропустить;
// error — Redis недоступен. Решение, что делать с ошибкой, принимает
// вызывающий: для уборки правильный ответ — пропустить цикл (лучше не убраться,
// чем убраться дважды), и именно так поступают оба текущих потребителя.
func (l *Lock) TryLock(ctx context.Context, ttl time.Duration) (bool, error) {
	if l == nil || l.client == nil {
		// Нет Redis — нет и второй реплики, которую надо разводить (одиночная
		// установка, тесты). Разрешаем цикл: иначе выключение Redis тихо
		// остановило бы всю периодическую уборку.
		return true, nil
	}
	ok, err := l.client.SetNX(ctx, l.key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("redislock %s: %w", l.key, err)
	}
	return ok, nil
}

// ── Lease: лок с владельцем и продлением ─────────────────────────────────────

// acquireSrc — атомарно «взять, если свободен, либо продлить, если мой».
//
// Три ветки вместо SET NX: обычный SETNX умеет только взять и не умеет
// продлить, а продление через отдельный EXPIRE — это гонка (между GET и EXPIRE
// лок успевает истечь и уйти соседу, которому мы затем продлеваем чужой лок).
// Скрипт выполняется в Redis целиком, поэтому середины у него нет.
const acquireSrc = `
local v = redis.call('GET', KEYS[1])
if v == false then
	redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
	return 1
elseif v == ARGV[1] then
	redis.call('PEXPIRE', KEYS[1], ARGV[2])
	return 1
end
return 0
`

// releaseSrc — удалить лок, только если он ещё наш. Слепой DEL снёс бы чужой
// лок, если наш успел истечь и его перехватила соседняя реплика.
const releaseSrc = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`

// Lease — аренда именованного ресурса конкретным владельцем (§93.8).
//
// Отличие от [Lock]: аренду можно продлевать, пока владелец жив, и отдавать при
// штатной остановке. Это то, что нужно «одна реплика ведёт этот узел»: лок без
// продления пришлось бы брать заново каждый цикл, и ведущий постоянно менялся
// бы, а лок без владельца нельзя безопасно отдать.
//
// Владелец — имя реплики (hostname). После перезапуска контейнера оно то же,
// поэтому реплика возвращает себе свои же ресурсы, не дожидаясь TTL.
type Lease struct {
	client *goredis.Client
	owner  string
	// Скрипты — поля, а не package-level переменные (CLAUDE.md §4: никакого
	// изменяемого состояния уровня пакета). goredis.Script кэширует SHA после
	// первого EVALSHA, то есть у объекта есть состояние, и общий на пакет
	// экземпляр был бы именно таким глобальным состоянием.
	acquire *goredis.Script
	release *goredis.Script
}

// NewLease создаёт аренду от имени owner. Пустой owner допустим (одиночная
// установка) — тогда аренда вырождается в «всегда мой».
func NewLease(client *goredis.Client, owner string) *Lease {
	return &Lease{
		client:  client,
		owner:   owner,
		acquire: goredis.NewScript(acquireSrc),
		release: goredis.NewScript(releaseSrc),
	}
}

// leaseKey — nexus:lock:<name>. Общий префикс с Lock: оба видны одним SCAN.
func leaseKey(name string) string { return KeyPrefix + name }

// Acquire берёт аренду ресурса или продлевает уже свою.
//
// true  — ресурс наш ещё на ttl;
// false — им владеет другая реплика;
// error — Redis недоступен.
//
// Без клиента или без владельца возвращает true: в одиночной установке делить
// ресурс не с кем, и отсутствие Redis не должно останавливать работу.
func (l *Lease) Acquire(ctx context.Context, name string, ttl time.Duration) (bool, error) {
	if l == nil || l.client == nil || l.owner == "" {
		return true, nil
	}
	res, err := l.acquire.Run(ctx, l.client, []string{leaseKey(name)}, l.owner, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("redislock acquire %s: %w", leaseKey(name), err)
	}
	return res == 1, nil
}

// Release отдаёт аренду, если она ещё наша.
//
// Вызывается при штатной остановке: без этого соседняя реплика ждала бы
// истечения TTL, и ресурс на это время оставался бы без ведущего — при выкате
// это заметная пауза в обработке.
func (l *Lease) Release(ctx context.Context, name string) error {
	if l == nil || l.client == nil || l.owner == "" {
		return nil
	}
	if err := l.release.Run(ctx, l.client, []string{leaseKey(name)}, l.owner).Err(); err != nil &&
		!errors.Is(err, goredis.Nil) {
		return fmt.Errorf("redislock release %s: %w", leaseKey(name), err)
	}
	return nil
}
