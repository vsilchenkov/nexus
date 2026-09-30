package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// TestIdentityArgs — Create не берёт id и даты из узла (их выдаёт база), а
// восстановление §110 берёт, подставляя значения по умолчанию только вместо
// пустых.
func TestIdentityArgs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	full := &domain.Node{ID: "11111111-1111-1111-1111-111111111111", CreatedAt: at, UpdatedAt: at.Add(time.Hour)}

	id, c, u := identityArgs(full, false)
	assert.Nil(t, id, "Create игнорирует id узла")
	assert.Nil(t, c)
	assert.Nil(t, u)

	id, c, u = identityArgs(full, true)
	require.NotNil(t, id)
	assert.Equal(t, full.ID, *id)
	require.NotNil(t, c)
	assert.Equal(t, at, *c)
	require.NotNil(t, u)
	assert.Equal(t, at.Add(time.Hour), *u)

	id, c, u = identityArgs(&domain.Node{}, true)
	assert.Nil(t, id, "пустой id — значение по умолчанию базы")
	assert.Nil(t, c)
	assert.Nil(t, u)
}
