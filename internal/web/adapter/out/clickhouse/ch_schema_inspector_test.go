package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTTLDaysFromCreate — извлечение срока native-TTL из SHOW CREATE TABLE.
// Регрессия §56: ClickHouse нормализует записанный нами `INTERVAL <n> DAY` в
// `toIntervalDay(<n>)`, и старый regex `INTERVAL\s+(\d+)\s+DAY` его не матчил —
// интроспекция возвращала HasTTL=false, а планировщик предлагал MODIFY TTL по
// кругу (retention «не применяется»). Обе формы должны давать один и тот же день.
func TestTTLDaysFromCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		createSQL string
		wantDays  int32
		wantOK    bool
	}{
		{
			// Точная форма из живого стенда (узел cdsac, nexus_rtt.cdsac):
			// именно она ломала цикл apply→снова до фикса.
			name:      "toIntervalDay normalized by ClickHouse",
			createSQL: "CREATE TABLE nexus_rtt.cdsac (`ID` String)\nENGINE = MergeTree\nTTL date_create + toIntervalDay(50)\nSETTINGS index_granularity = 8192",
			wantDays:  50,
			wantOK:    true,
		},
		{
			name:      "INTERVAL N DAY literal form",
			createSQL: "CREATE TABLE t (`ID` String) ENGINE = MergeTree TTL date_create + INTERVAL 90 DAY DELETE",
			wantDays:  90,
			wantOK:    true,
		},
		{
			name:      "toIntervalDay with inner spaces",
			createSQL: "TTL date_create + toIntervalDay( 7 )",
			wantDays:  7,
			wantOK:    true,
		},
		{
			name:      "no native TTL",
			createSQL: "CREATE TABLE t (`ID` String) ENGINE = MergeTree ORDER BY ID SETTINGS index_granularity = 8192",
			wantDays:  0,
			wantOK:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			days, ok := ttlDaysFromCreate(tt.createSQL)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantDays, days)
		})
	}
}
