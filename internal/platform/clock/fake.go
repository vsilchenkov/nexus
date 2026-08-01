package clock

import (
	"sync"
	"time"
)

// Fake — управляемые часы для тестов: время двигает сам тест, а не sleep.
// Безопасен для конкурентного чтения: фоновые воркеры под тестом читают Now()
// из своих горутин.
type Fake struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFake создаёт часы, стоящие на t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now возвращает текущее «время» часов.
func (f *Fake) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

// Advance двигает часы вперёд на d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set устанавливает точное время.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}
