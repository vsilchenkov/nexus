package usecase

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		// §84.1: три окна, где деление на 48/84/90 давало некруглый шаг.
		// На старом коде здесь 225, 13440 и 28800 соответственно.
		{
			name:   "§84.1: 3 часа — 5 минут вместо 3 мин 45 с",
			window: 3 * time.Hour, step: 0,
			wantStepSec: 300, wantBuckets: 36,
		},
		{
			name:   "§84.1: 14 суток — 6 часов вместо 3 ч 44 мин",
			window: 14 * day, step: 0,
			wantStepSec: 21600, wantBuckets: 56,
		},
		{
			name:   "§84.1: 30 суток — 12 часов вместо 8",
			window: 30 * day, step: 0,
			wantStepSec: 43200, wantBuckets: 60,
		},
		{
			name:   "§84.1: лестница исчерпана — шаг остаётся кратным суткам",
			window: 100 * day, step: 0,
			wantStepSec: 2 * 86400, wantBuckets: 50,
		},
		{
			name:   "§84.1: окно короче нижней ступени сжимается до окна, а не даёт столбец шире периода",
			window: 30 * time.Second, step: 0,
			wantStepSec: 30, wantBuckets: 1,
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

// autoStepWindows — окна для проверки инвариантов авто-шага: все пресеты UI
// плюс набор произвольных периодов, которые пользователь может задать
// календарём (§84.4). Случайные величины не берём намеренно: падение теста
// обязано воспроизводиться с первого раза.
func autoStepWindows() []time.Duration {
	const day = 24 * time.Hour
	presets := []time.Duration{time.Hour, 3 * time.Hour, day, 7 * day, 14 * day, 30 * day}
	out := make([]time.Duration, 0, 31)
	out = append(out, presets...)
	for _, m := range []int{1, 2, 5, 7, 13, 47, 90, 240, 500, 1000} {
		out = append(out, time.Duration(m)*time.Minute)
	}
	for _, h := range []int{2, 5, 9, 17, 36, 100, 500} {
		out = append(out, time.Duration(h)*time.Hour)
	}
	for _, d := range []int{2, 3, 10, 21, 45, 95, 120, 365} {
		out = append(out, time.Duration(d)*day)
	}
	return out
}

// §84.1: авто-шаг обязан делить сутки нацело — иначе toStartOfInterval,
// считающий от UTC-эпохи, режет сутки в произвольных точках, и границы
// столбцов не совпадают с отметками часов. На старом коде падает на 3 ч (225)
// и 14 сутках (13440).
func TestAutoStepIsRound(t *testing.T) {
	t.Parallel()
	for _, w := range autoStepWindows() {
		t.Run(w.String(), func(t *testing.T) {
			t.Parallel()
			stepSec, _ := resolveChartStep(w, 0)
			require.Positive(t, stepSec)
			// Либо шаг делит сутки, либо (на окнах больше лестницы) сам кратен суткам.
			ok := 86400%stepSec == 0 || stepSec%86400 == 0
			// Окно короче нижней ступени сжимается до самого окна — там
			// круглость невыразима и не нужна: столбец ровно один.
			if w < time.Minute {
				ok = true
			}
			assert.Truef(t, ok, "шаг %d с не выравнивается по суткам на окне %s", stepSec, w)
		})
	}
}

// §84.1: снап только ВВЕРХ. Столбцов не должно становиться больше целевой
// плотности §79.5 — иначе поллинг вкладки начнёт грузить ClickHouse сильнее,
// чем до раздела.
func TestAutoStepNeverDenserThanBefore(t *testing.T) {
	t.Parallel()
	for _, w := range autoStepWindows() {
		t.Run(w.String(), func(t *testing.T) {
			t.Parallel()
			_, buckets := resolveChartStep(w, 0)
			assert.LessOrEqualf(t, buckets, autoChartBuckets(w),
				"окно %s: столбцов стало больше целевой плотности", w)
		})
	}
}

// §84.1: лестница задаётся руками, и опечатка в ней тихо ломает выравнивание
// на конкретном окне. Проверяем сам список, а не только его следствия.
func TestChartStepLadderDividesDay(t *testing.T) {
	t.Parallel()
	var prev time.Duration
	for _, s := range chartStepLadder {
		sec := int64(s.Seconds())
		assert.Zerof(t, 86400%sec, "ступень %s не делит сутки нацело", s)
		assert.Greaterf(t, s, prev, "лестница обязана строго возрастать: %s после %s", s, prev)
		prev = s
	}
}
