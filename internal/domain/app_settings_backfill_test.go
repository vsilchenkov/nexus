package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nexus/internal/domain"
)

// §106.4: nil — дефолт, "" — выключено, иначе значение как есть. Web и Sender
// обязаны понимать nil одинаково, поэтому разворачивание — в домене.
func TestClientHostBackfillCronOrDefault(t *testing.T) {
	t.Parallel()
	assert.Equal(t, domain.ClientHostBackfillDefaultCron, domain.ClientHostBackfillCronOrDefault(nil))
	assert.Empty(t, domain.ClientHostBackfillCronOrDefault(new("")))
	assert.Equal(t, "*/5 * * * *", domain.ClientHostBackfillCronOrDefault(new("*/5 * * * *")))
}

// «Выключено» переживает запись в JSONB: omitempty выбрасывает nil-указатель,
// но не указатель на пустую строку. Иначе после сохранения "" Sender прочитал
// бы nil и включил расписание по умолчанию.
func TestGeneralSettings_ClientHostBackfillCron_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range []*string{nil, new(""), new("0 3 * * *")} {
		raw, err := json.Marshal(domain.GeneralSettings{ClientHostBackfillCron: v})
		require.NoError(t, err)
		var got domain.GeneralSettings
		require.NoError(t, json.Unmarshal(raw, &got))
		if v == nil {
			assert.Nil(t, got.ClientHostBackfillCron, "json=%s", raw)
			continue
		}
		require.NotNil(t, got.ClientHostBackfillCron, "json=%s", raw)
		assert.Equal(t, *v, *got.ClientHostBackfillCron)
	}
}
