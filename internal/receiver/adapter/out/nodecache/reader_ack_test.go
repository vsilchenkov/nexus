package nodecache

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
	"nexus/internal/domain/ackspec"
)

// TestSelectIncludesAsyncAckSpec — Receiver ведёт СВОЙ, урезанный список
// колонок, и забытая в нём колонка означает фичу, которая молча не работает в
// бою при зелёных тестах Web. Прецедент уже есть: incoming_auth_dynamic_* в
// этот SELECT так и не попали. Тест дешёвый и ловит ровно этот класс ошибок.
func TestSelectIncludesAsyncAckSpec(t *testing.T) {
	t.Parallel()

	assert.Contains(t, pgSelectNodeByTeamSlugAndPath, "async_ack_spec",
		"колонка §83 обязана быть в SELECT Receiver'а, иначе шаблон ответа никогда не применится")
}

// TestNodeCacheRoundTrip_AsyncAck — узел едет в Redis как JSON целиком.
// Спека обязана пережить этот путь: иначе Receiver прочитает её из PG один раз,
// а следующие запросы (из кеша) ответят по-старому — плавающее поведение,
// которое на стенде почти не воспроизводится.
func TestNodeCacheRoundTrip_AsyncAck(t *testing.T) {
	t.Parallel()

	src := &domain.Node{
		Path:       "acs_sigur",
		RootMethod: domain.RootMethodRequestAsync,
		AsyncAck: &ackspec.Spec{
			Version:     ackspec.Version,
			ContentType: ackspec.ContentTypeJSON,
			Body:        `{"confirmedLogId": "${ body.logs[*].logId | max }"}`,
			OnError:     ackspec.OnErrorDefault,
		},
	}

	raw, err := json.Marshal(src)
	require.NoError(t, err)

	var got domain.Node
	require.NoError(t, json.Unmarshal(raw, &got))

	require.NotNil(t, got.AsyncAck, "спека потеряна в кеш-раунд-трипе")
	assert.Equal(t, src.AsyncAck.Body, got.AsyncAck.Body)
	assert.Equal(t, ackspec.ContentTypeJSON, got.AsyncAck.ContentType)
	assert.Equal(t, ackspec.OnErrorDefault, got.AsyncAck.OnError)
}

// TestNodeCacheRoundTrip_PreAckEntry — запись кеша, сделанная бинарём до §83,
// обязана давать nil-спеку (прежний ответ), а не ломать разбор узла: на выкате
// такие записи живут до истечения TTL.
func TestNodeCacheRoundTrip_PreAckEntry(t *testing.T) {
	t.Parallel()

	var got domain.Node
	require.NoError(t, json.Unmarshal(
		[]byte(`{"Path":"acs_sigur","RootMethod":"requestAsync","LoggingEnabled":true}`), &got))

	assert.Nil(t, got.AsyncAck)
	assert.Equal(t, "acs_sigur", got.Path)
}

// TestNodeCacheRoundTrip_BrokenSpecIsIgnored — битый JSON в кеше не должен
// ронять разбор узла целиком (то же правило, что и для колонки в PG).
func TestNodeCacheRoundTrip_BrokenSpecIsIgnored(t *testing.T) {
	t.Parallel()

	var got domain.Node
	err := json.Unmarshal([]byte(`{"Path":"n","AsyncAck":"not-an-object"}`), &got)

	// Разбор такого кеша падает — и это корректно: Get трактует ошибку
	// json.Unmarshal как cache-miss и идёт в PostgreSQL за свежим узлом.
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "AsyncAck") ||
		strings.Contains(err.Error(), "ackspec.Spec"),
		"ошибка должна указывать на спеку, got %v", err)
}
