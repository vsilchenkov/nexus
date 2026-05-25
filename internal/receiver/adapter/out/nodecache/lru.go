// Package nodecache.
//
// lru.go — простой потокобезопасный LRU-кеш с TTL для использования в L2-слое
// чтения конфига узлов (§9.2 ТЗ: «локальный second-level cache, 1-5 сек,
// ~1000 узлов»). Реализация без внешних зависимостей: одна map + двусвязный
// список под LRU-порядок.
//
// Семантика:
//   - Get возвращает значение, только если запись не протухла (now < expiresAt).
//   - GetStale возвращает запись даже после истечения TTL, вместе с возрастом —
//     для §9.4 «PostgreSQL и Redis недоступны одновременно», когда лучше
//     отдать чуть-чуть устаревший конфиг, чем 503.
//   - Set перезаписывает значение, обновляет TTL и переносит запись в head.
//   - При превышении size вытесняется самый «холодный» элемент (tail).
package nodecache

import (
	"container/list"
	"sync"
	"time"
)

// Clock — крошечная инъекция для тестов (чтобы не ждать реального TTL).
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// LRU — потокобезопасный LRU-кеш с TTL. Параметризован значением V.
//
// Не используем generics поверх sync.Map: явные мьютекс + map + список
// дают точный контроль над порядком и предсказуемую сложность O(1) для
// Get/Set/Delete.
type LRU[V any] struct {
	mu     sync.Mutex
	items  map[string]*list.Element
	order  *list.List
	size   int
	ttl    time.Duration
	clock  Clock
	onEvict func() // best-effort callback для метрик; nil допустим
}

type lruEntry[V any] struct {
	key       string
	value     V
	expiresAt time.Time
}

// NewLRU создаёт пустой кеш на size элементов с TTL ttl.
// size ≤ 0 или ttl ≤ 0 → паника (нонсенс-конфиг ловим как можно раньше).
func NewLRU[V any](size int, ttl time.Duration) *LRU[V] {
	if size <= 0 {
		panic("nodecache: LRU size must be > 0")
	}
	if ttl <= 0 {
		panic("nodecache: LRU ttl must be > 0")
	}
	return &LRU[V]{
		items: make(map[string]*list.Element, size),
		order: list.New(),
		size:  size,
		ttl:   ttl,
		clock: realClock{},
	}
}

// WithClock подменяет источник времени (для тестов).
func (c *LRU[V]) WithClock(clock Clock) *LRU[V] {
	c.clock = clock
	return c
}

// WithOnEvict регистрирует callback, вызываемый при каждом вытеснении из-за
// переполнения. Используем для счётчика метрики.
func (c *LRU[V]) WithOnEvict(fn func()) *LRU[V] {
	c.onEvict = fn
	return c
}

// Get возвращает свежее значение (fresh, не протухло). Найдено и not-expired →
// (v, true). Иначе (zero, false).
func (c *LRU[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	entry := el.Value.(*lruEntry[V])
	if c.clock.Now().After(entry.expiresAt) {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return entry.value, true
}

// GetStale возвращает запись, даже если TTL истёк. Второй аргумент — fresh
// (true, если ещё не протухла). Третий — возраст записи относительно now.
// Используется в §9.4 «крайний случай» fallback'е.
func (c *LRU[V]) GetStale(key string) (value V, fresh bool, age time.Duration, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, exists := c.items[key]
	if !exists {
		return value, false, 0, false
	}
	entry := el.Value.(*lruEntry[V])
	now := c.clock.Now()
	fresh = !now.After(entry.expiresAt)
	age = now.Sub(entry.expiresAt.Add(-c.ttl))
	if age < 0 {
		age = 0
	}
	if fresh {
		c.order.MoveToFront(el)
	}
	return entry.value, fresh, age, true
}

// Set сохраняет значение с TTL, рассчитанным от текущего времени.
// При переполнении вытесняет самый холодный элемент.
func (c *LRU[V]) Set(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiresAt := c.clock.Now().Add(c.ttl)
	if el, ok := c.items[key]; ok {
		entry := el.Value.(*lruEntry[V])
		entry.value = value
		entry.expiresAt = expiresAt
		c.order.MoveToFront(el)
		return
	}
	entry := &lruEntry[V]{key: key, value: value, expiresAt: expiresAt}
	el := c.order.PushFront(entry)
	c.items[key] = el

	if c.order.Len() > c.size {
		c.evictOldest()
	}
}

// Delete удаляет запись по ключу. No-op если ключа нет.
func (c *LRU[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
		delete(c.items, key)
	}
}

// Len возвращает количество записей (включая возможно-протухшие).
func (c *LRU[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *LRU[V]) evictOldest() {
	tail := c.order.Back()
	if tail == nil {
		return
	}
	entry := tail.Value.(*lruEntry[V])
	c.order.Remove(tail)
	delete(c.items, entry.key)
	if c.onEvict != nil {
		c.onEvict()
	}
}
