package usecase

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSessionTTLProvider_FallbackAndSet(t *testing.T) {
	t.Parallel()
	p := NewSessionTTLProvider(86400) // fallback 24ч

	// До Set — fallback из env.
	assert.Equal(t, 24*time.Hour, p.Get())

	// Set задаёт новое значение.
	p.Set(3600)
	assert.Equal(t, time.Hour, p.Get())

	// Set(0) — сброс на fallback.
	p.Set(0)
	assert.Equal(t, 24*time.Hour, p.Get())

	// Отрицательное — тоже fallback.
	p.Set(-5)
	assert.Equal(t, 24*time.Hour, p.Get())
}

// TestSessionTTLProvider_Concurrent — гонок нет (atomic). Запускать с -race.
func TestSessionTTLProvider_Concurrent(t *testing.T) {
	t.Parallel()
	p := NewSessionTTLProvider(60)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); p.Set(300) }()
		go func() { defer wg.Done(); _ = p.Get() }()
	}
	wg.Wait()
	assert.Equal(t, 300*time.Second, p.Get())
}
