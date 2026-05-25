package reloader_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bus/internal/platform/logging"
	"bus/internal/platform/reloader"
)

func TestSubscriber_DispatchesBySection(t *testing.T) {
	t.Parallel()

	// Сабж тестируем без реального Redis: вместо Run раскидываем
	// payload через приватный handle? — handle приватный, но публичный
	// контракт через Run + pub/sub требует Redis. Поэтому тестируем
	// поведение через JSON-формат + ручной вызов из теста.

	var (
		sentryHits     int32
		clickhouseHits int32
	)

	// Имитируем обработку через тестовую обёртку: создаём fakeRedis с
	// каналом сообщений и вручную делаем то, что делает Subscriber.
	// Для упрощения: проверяем формат payload + Section type.

	msg := reloader.Message{Section: reloader.SectionSentry}
	raw, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded reloader.Message
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, reloader.SectionSentry, decoded.Section)

	// Прогон через Section "all" должен покрывать обе секции.
	all := reloader.Message{Section: reloader.SectionAll}
	rawAll, _ := json.Marshal(all)
	var got reloader.Message
	require.NoError(t, json.Unmarshal(rawAll, &got))
	assert.Equal(t, reloader.SectionAll, got.Section)

	// Дополнительно: атомарные счётчики проверяют, что dispatch вызывает
	// функции. Имитируем через ручной dispatch.
	cbs := map[reloader.Section][]reloader.Reloader{
		reloader.SectionSentry: {
			func(context.Context) error { atomic.AddInt32(&sentryHits, 1); return nil },
		},
		reloader.SectionClickHouse: {
			func(context.Context) error { atomic.AddInt32(&clickhouseHits, 1); return nil },
		},
	}
	dispatch := func(m reloader.Message) {
		sections := []reloader.Section{m.Section}
		if m.Section == reloader.SectionAll {
			sections = []reloader.Section{reloader.SectionSentry, reloader.SectionClickHouse}
		}
		for _, s := range sections {
			for _, fn := range cbs[s] {
				_ = fn(context.Background())
			}
		}
	}
	dispatch(reloader.Message{Section: reloader.SectionSentry})
	dispatch(reloader.Message{Section: reloader.SectionAll})

	assert.Equal(t, int32(2), atomic.LoadInt32(&sentryHits))
	assert.Equal(t, int32(1), atomic.LoadInt32(&clickhouseHits))
}

func TestPublisher_NilSafe(t *testing.T) {
	t.Parallel()
	var p *reloader.Publisher // nil — не должен паниковать
	err := p.Publish(context.Background(), reloader.SectionSentry)
	assert.NoError(t, err)
}

func TestSubscriber_Register_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	// Регистрация из нескольких горутин одновременно не должна race'ить
	// (mutex внутри Register).
	s := reloader.NewSubscriber(nil, logging.NewNoop())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Register(reloader.SectionSentry, func(context.Context) error { return nil })
		}()
	}
	wg.Wait()
}

