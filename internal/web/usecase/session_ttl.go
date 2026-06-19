package usecase

import (
	"sync/atomic"
	"time"
)

// SessionTTLProvider — атомарный держатель текущей длительности сессии (§34.2).
//
// Хранит TTL в секундах. На старте сидится значением из app_settings (если
// задано) либо остаётся на fallback из env-конфига (cfg.Redis.SessionTTLSec).
// Обновляется hot-reload'ом (секция security) без рестарта — новые сессии и
// sliding-Touch берут актуальное значение через Get.
//
// Безопасен для конкурентного доступа: чтение/запись через atomic.Int64.
type SessionTTLProvider struct {
	seconds  atomic.Int64
	fallback int
}

// NewSessionTTLProvider создаёт провайдер с fallback-значением (секунды) из
// env-конфига. До первого Set Get возвращает fallback.
func NewSessionTTLProvider(fallbackSeconds int) *SessionTTLProvider {
	return &SessionTTLProvider{fallback: fallbackSeconds}
}

// Get возвращает текущую длительность сессии. Если значение не задано (0) —
// fallback из env-конфига.
func (p *SessionTTLProvider) Get() time.Duration {
	s := p.seconds.Load()
	if s <= 0 {
		s = int64(p.fallback)
	}
	return time.Duration(s) * time.Second
}

// Set задаёт новое значение TTL (секунды). 0 или отрицательное — сбрасывает на
// fallback (Get вернёт env-значение).
func (p *SessionTTLProvider) Set(seconds int) {
	p.seconds.Store(int64(seconds))
}
