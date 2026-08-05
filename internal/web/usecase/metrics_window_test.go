package usecase

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// §79.5: «Шаг графика» — ширина столбца задаётся пользователем. Здесь
// проверяется согласование пары (окно, шаг): без него можно получить пустой
// график, столбец за границей периода или тысячи столбцов по 1 px.

func TestParseChartStep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"auto", 0},
		{"AUTO", 0},
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"14d", 14 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
		{"30m", 30 * time.Minute}, // Go-длительность для клиентов API
		{"мусор", 0},              // не 400: необязательный параметр графика
		{"-1h", 0},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ParseChartStep(tt.in))
		})
	}
}

func TestResolveChartStep(t *testing.T) {
	t.Parallel()
	const day = 24 * time.Hour
	tests := []struct {
		name        string
		window      time.Duration
		step        time.Duration
		wantStepSec int64
		wantBuckets int
	}{
		{
			name:   "сценарий раздела: 14 дней по суткам = 14 столбцов",
			window: 14 * day, step: day,
			wantStepSec: 86400, wantBuckets: 14,
		},
		{
			name:   "шаг не задан — прежнее поведение (48 столбцов на сутки)",
			window: day, step: 0,
			wantStepSec: 1800, wantBuckets: 48,
		},
		{
			name:   "шаг не задан на часе — 60 столбцов по минуте",
			window: time.Hour, step: 0,
			wantStepSec: 60, wantBuckets: 60,
		},
		{
			name:   "шаг больше окна сжимается до окна: один столбец, а не пустой график",
			window: day, step: 7 * day,
			wantStepSec: 86400, wantBuckets: 1,
		},
		{
			name:   "слишком много столбцов — шаг поднимается до потолка",
			window: 30 * day, step: time.Hour,
			wantStepSec: int64((30 * day / maxChartBuckets).Seconds()), wantBuckets: maxChartBuckets,
		},
		{
			name:   "вырожденное окно не роняет расчёт",
			window: 0, step: time.Hour,
			wantStepSec: 1, wantBuckets: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stepSec, buckets := resolveChartStep(tt.window, tt.step)
			assert.Equal(t, tt.wantStepSec, stepSec, "шаг в секундах")
			assert.Equal(t, tt.wantBuckets, buckets, "число столбцов")
			assert.LessOrEqual(t, buckets, maxChartBuckets, "потолок столбцов обязан соблюдаться всегда")
		})
	}
}
