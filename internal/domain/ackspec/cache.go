package ackspec

import (
	"container/list"
	"sync"
)

// DefaultCacheSize — сколько шаблонов держим скомпилированными. Шаблон один на
// узел, поэтому размер порядка числа активных async-узлов инсталляции.
const DefaultCacheSize = 256

type cacheEntry struct {
	key  string
	tmpl *Template
	err  error
}

// Cache — LRU скомпилированных шаблонов, ключ — сам текст шаблона.
//
// Узел приезжает в Receiver из L1/Redis-кеша строкой, а компиляция на каждый
// принятый запрос — лишняя работа на горячем пути. Ключ по содержимому, а не по
// id узла, снимает вопрос инвалидации: правка шаблона просто даёт другой ключ.
//
// Ошибки компиляции кешируются наравне с успехом — иначе битая спека узла с
// высоким трафиком перепарсивалась бы сотни раз в секунду.
type Cache struct {
	mu    sync.Mutex
	size  int
	ll    *list.List
	items map[string]*list.Element
}

// NewCache создаёт кеш. size <= 0 — DefaultCacheSize.
func NewCache(size int) *Cache {
	if size <= 0 {
		size = DefaultCacheSize
	}
	return &Cache{
		size:  size,
		ll:    list.New(),
		items: make(map[string]*list.Element, size),
	}
}

// Get возвращает скомпилированный шаблон, компилируя его при промахе.
// Возвращённый *Template иммутабелен и безопасен для параллельного рендера.
func (c *Cache) Get(tmpl string, ct ContentType) (*Template, error) {
	key := string(ct) + "\x00" + tmpl
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		c.mu.Unlock()
		return e.tmpl, e.err
	}
	c.mu.Unlock()

	// Компиляция вне блокировки: параллельный промах по одному ключу приведёт к
	// двойной работе, но не к остановке остальных узлов на чужом шаблоне.
	t, err := Compile(tmpl, ct)

	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok { // кто-то успел положить первым
		c.ll.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		return e.tmpl, e.err
	}
	c.items[key] = c.ll.PushFront(&cacheEntry{key: key, tmpl: t, err: err})
	if c.ll.Len() > c.size {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
	return t, err
}

// Len — сколько шаблонов сейчас в кеше (для тестов и диагностики).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
