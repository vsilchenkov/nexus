package logsink

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"nexus/internal/platform/sensitive"
)

// Дефолтные ёмкости кольца и канала шиппера (§51.3 ТЗ).
const (
	DefaultRingCapacity = 2000
	DefaultChanCapacity = 1024
)

// RingOption настраивает RingHandler при создании.
type RingOption func(*ringCore)

// WithRingCapacity задаёт ёмкость in-process кольцевого буфера.
func WithRingCapacity(n int) RingOption {
	return func(c *ringCore) {
		if n > 0 {
			c.buf = make([]Entry, 0, n)
		}
	}
}

// WithInstance проставляет идентификатор ноды в каждую запись (§70.7). Пустое
// значение поле не добавляет.
func WithInstance(id string) RingOption {
	return func(c *ringCore) { c.instance = id }
}

// WithReplica проставляет имя реплики в каждую запись (§93.6). Пустое значение
// поле не добавляет — одиночная установка выглядит как прежде.
func WithReplica(name string) RingOption {
	return func(c *ringCore) { c.replica = name }
}

// WithChannelCapacity задаёт ёмкость буферизованного канала к шипперу.
func WithChannelCapacity(n int) RingOption {
	return func(c *ringCore) {
		if n > 0 {
			c.ch = make(chan Entry, n)
		}
	}
}

// ringCore — состояние, разделяемое всеми клонами хендлера (WithAttrs/WithGroup
// возвращают новый RingHandler поверх того же core).
type ringCore struct {
	lv      *slog.LevelVar
	service string
	// instance — идентификатор ноды (§70.7); пустой у ноды до §70.
	instance string
	// replica — имя реплики сервиса (§93.6); пустое в одиночной установке.
	replica string

	mu   sync.Mutex
	buf  []Entry // кольцо: buf[next] — место следующей записи
	next int
	full bool

	ch      chan Entry
	dropped atomic.Uint64
}

// boundAttr — атрибут, привязанный через WithAttrs, вместе с group-путём,
// действовавшим в момент привязки (семантика slog: WithGroup влияет на
// последующие атрибуты).
type boundAttr struct {
	groups []string
	attr   slog.Attr
}

// RingHandler — slog.Handler консоли служебных логов: маскирует значения
// чувствительных атрибутов ("***"), кладёт запись в кольцевой буфер и
// неблокирующе отправляет её в канал шиппера. Никакого IO в Handle.
type RingHandler struct {
	core   *ringCore
	bound  []boundAttr
	groups []string
}

var _ slog.Handler = (*RingHandler)(nil)

// NewRingHandler создаёт хендлер с порогом level (общий *slog.LevelVar цепочки)
// и тегом сервиса (receiver|sender|web).
func NewRingHandler(lv *slog.LevelVar, service string, opts ...RingOption) *RingHandler {
	core := &ringCore{
		lv:      lv,
		service: service,
		buf:     make([]Entry, 0, DefaultRingCapacity),
		ch:      make(chan Entry, DefaultChanCapacity),
	}
	for _, o := range opts {
		o(core)
	}
	return &RingHandler{core: core}
}

// Enabled следует текущему значению LevelVar — уровень меняется в runtime.
func (h *RingHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.core.lv.Level()
}

// Handle собирает Entry (с маскированием), пишет в кольцо и неблокирующе
// отправляет шипперу; при полном канале — дроп + счётчик.
func (h *RingHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	for _, ba := range h.bound {
		putAttr(attrs, ba.groups, ba.attr)
	}
	r.Attrs(func(a slog.Attr) bool {
		putAttr(attrs, h.groups, a)
		return true
	})
	if len(attrs) == 0 {
		attrs = nil
	}
	e := Entry{
		TS:       r.Time,
		Level:    strings.ToLower(r.Level.String()),
		Service:  h.core.service,
		Instance: h.core.instance,
		Replica:  h.core.replica,
		Msg:      r.Message,
		Attrs:    attrs,
	}

	c := h.core
	c.mu.Lock()
	if len(c.buf) < cap(c.buf) {
		c.buf = append(c.buf, e)
	} else {
		c.buf[c.next] = e
		c.full = true
	}
	c.next = (c.next + 1) % cap(c.buf)
	c.mu.Unlock()

	select {
	case c.ch <- e:
	default:
		c.dropped.Add(1)
	}
	return nil
}

// WithAttrs возвращает клон с привязанными атрибутами (общий core).
func (h *RingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := h.clone()
	for _, a := range attrs {
		nh.bound = append(nh.bound, boundAttr{groups: h.groups, attr: a})
	}
	return nh
}

// WithGroup возвращает клон с добавленной группой (общий core).
func (h *RingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := h.clone()
	nh.groups = append(nh.groups, name)
	return nh
}

func (h *RingHandler) clone() *RingHandler {
	nh := &RingHandler{
		core:   h.core,
		bound:  make([]boundAttr, len(h.bound), len(h.bound)+1),
		groups: make([]string, len(h.groups), len(h.groups)+1),
	}
	copy(nh.bound, h.bound)
	copy(nh.groups, h.groups)
	return nh
}

// Entries — канал записей для шиппера (read-only).
func (h *RingHandler) Entries() <-chan Entry { return h.core.ch }

// Service возвращает тег сервиса хендлера.
func (h *RingHandler) Service() string { return h.core.service }

// Dropped — сколько записей дропнуто из-за переполнения канала шиппера.
func (h *RingHandler) Dropped() uint64 { return h.core.dropped.Load() }

// Snapshot возвращает копию кольца от старейшей записи к новейшей.
func (h *RingHandler) Snapshot() []Entry {
	c := h.core
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.full {
		out := make([]Entry, len(c.buf))
		copy(out, c.buf)
		return out
	}
	out := make([]Entry, 0, cap(c.buf))
	out = append(out, c.buf[c.next:]...)
	out = append(out, c.buf[:c.next]...)
	return out
}

// putAttr раскладывает slog.Attr в карту attrs по group-пути: группы дают
// вложенные map'ы, значения чувствительных ключей заменяются на "***",
// длинные строки обрезаются.
func putAttr(dst map[string]any, groups []string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		if len(members) == 0 {
			return
		}
		path := groups
		if a.Key != "" {
			path = append(append([]string(nil), groups...), a.Key)
		}
		for _, m := range members {
			putAttr(dst, path, m)
		}
		return
	}
	m := descend(dst, groups)
	if sensitive.IsSensitive(a.Key) {
		m[a.Key] = "***"
		return
	}
	m[a.Key] = attrValue(a.Value)
}

// descend возвращает вложенную карту по group-пути, создавая уровни по мере
// необходимости. Конфликт «на этом ключе уже лежит не-map» решается заменой.
func descend(m map[string]any, groups []string) map[string]any {
	cur := m
	for _, g := range groups {
		next, ok := cur[g].(map[string]any)
		if !ok {
			next = make(map[string]any)
			cur[g] = next
		}
		cur = next
	}
	return cur
}

// attrValue приводит slog.Value к JSON-дружелюбному значению; строки режутся
// до maxAttrValueBytes.
func attrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return truncateValue(v.String())
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time()
	default:
		return truncateValue(v.String())
	}
}
