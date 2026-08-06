package ackspec_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain/ackspec"
)

func TestCache_ReturnsSameTemplate(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(4)
	const tmpl = `{"a": "${ body.x }"}`

	first, err := c.Get(tmpl, ackspec.ContentTypeJSON)
	require.NoError(t, err)
	second, err := c.Get(tmpl, ackspec.ContentTypeJSON)
	require.NoError(t, err)

	assert.Same(t, first, second, "hit must reuse the compiled template")
	assert.Equal(t, 1, c.Len())
}

// TestCache_ContentTypeIsPartOfKey — один и тот же текст в JSON и text/plain
// компилируется по-разному (quoted-режим), поэтому кеш обязан их различать.
func TestCache_ContentTypeIsPartOfKey(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(4)
	const tmpl = `"${ body.x }"`

	asJSON, err := c.Get(tmpl, ackspec.ContentTypeJSON)
	require.NoError(t, err)
	asText, err := c.Get(tmpl, ackspec.ContentTypeText)
	require.NoError(t, err)

	assert.NotSame(t, asJSON, asText)
	assert.Equal(t, 2, c.Len())

	jsonOut, err := asJSON.Render(jsonCtx(`{"x":7}`))
	require.NoError(t, err)
	textOut, err := asText.Render(jsonCtx(`{"x":7}`))
	require.NoError(t, err)
	assert.Equal(t, `7`, string(jsonOut))
	assert.Equal(t, `"7"`, string(textOut))
}

// TestCache_CachesErrors — битая спека узла с высоким трафиком не должна
// перекомпилироваться на каждом запросе.
func TestCache_CachesErrors(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(4)
	const broken = `{"a": "${ body.x"}`

	_, err1 := c.Get(broken, ackspec.ContentTypeJSON)
	_, err2 := c.Get(broken, ackspec.ContentTypeJSON)

	require.ErrorIs(t, err1, ackspec.ErrTemplateSyntax)
	assert.Equal(t, err1, err2)
	assert.Equal(t, 1, c.Len())
}

func TestCache_EvictsOldest(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(2)
	for i := range 5 {
		_, err := c.Get(`{"i": `+strconv.Itoa(i)+`}`, ackspec.ContentTypeJSON)
		require.NoError(t, err)
	}
	assert.Equal(t, 2, c.Len())
}

func TestCache_ZeroSizeFallsBackToDefault(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(0)
	for i := range ackspec.DefaultCacheSize + 10 {
		_, err := c.Get(`{"i": `+strconv.Itoa(i)+`}`, ackspec.ContentTypeJSON)
		require.NoError(t, err)
	}
	assert.Equal(t, ackspec.DefaultCacheSize, c.Len())
}

// TestCache_Parallel — кеш живёт в usecase и зовётся из всех горутин приёма.
// Гонки ловит -race, здесь фиксируется сам факт параллельного контракта.
func TestCache_Parallel(t *testing.T) {
	t.Parallel()

	c := ackspec.NewCache(8)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			tmpl := `{"i": "${ body.x }", "n": ` + strconv.Itoa(i%4) + `}`
			compiled, err := c.Get(tmpl, ackspec.ContentTypeJSON)
			assert.NoError(t, err)
			_, err = compiled.Render(jsonCtx(`{"x":1}`))
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, c.Len(), 8)
}
