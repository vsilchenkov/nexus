package usecase

import (
	"strings"
	"time"
)

// §79.5 «Шаг графика»: ширина одного столбца выбирается пользователем, а не
// выводится из длительности окна. Пара (окно, шаг) согласуется здесь —
// единственной чистой функцией, которую зовут и страница узла, и дашборд.

// maxChartBuckets — потолок числа столбцов. При ширине графика ~900 px это уже
// 2 px на столбец: рисовать плотнее — врать. Заодно ограничивает число групп в
// ClickHouse.
const maxChartBuckets = 400

// chartStepPresets — словарь шагов, общий с пресетами периода (§21). Значения
// вроде «7д» через time.ParseDuration не выражаются, поэтому карта своя.
var chartStepPresets = map[string]time.Duration{
	"1h":  time.Hour,
	"3h":  3 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// autoChartBuckets — сколько столбцов рисовать, когда шаг не задан. Прежнее
// поведение до §79.5, сохранено дословно: менять картинку у тех, кто шаг не
// трогал, раздел не обязывает.
func autoChartBuckets(window time.Duration) int {
	switch {
	case window <= time.Hour:
		return 60
	case window <= 24*time.Hour:
		return 48
	case window <= 7*24*time.Hour:
		return 84
	default:
		return 90
	}
}

// ParseChartStep — разбор параметра шага. Пустая строка и "auto" → 0
// («выбери сам»). Кроме пресетов принимается Go-длительность (`30m`, `90s`) —
// для клиентов API, которым словаря мало. Мусор → 0, а не ошибка: график не то
// место, где стоит отвечать 400 на кривой необязательный параметр.
func ParseChartStep(s string) time.Duration {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "auto" {
		return 0
	}
	if d, ok := chartStepPresets[s]; ok {
		return d
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return 0
}

// resolveChartStep — шаг бакета (в секундах) и число столбцов для окна.
//
// Правила согласования пары (окно, шаг):
//   - шаг не задан → прежний расчёт по длительности окна;
//   - шаг больше окна → шаг сжимается до окна: ровно один столбец, а не пустой
//     график и не столбец, торчащий за границу периода;
//   - столбцов вышло больше maxChartBuckets → шаг поднимается до окно/потолок.
//
// Возвращённый stepSec обязателен к выдаче наружу (step_seconds): иначе клиент
// вынужден угадывать ширину столбца по разнице соседних меток, а на ряде из
// одной точки угадать нечем.
func resolveChartStep(window, step time.Duration) (stepSec int64, buckets int) {
	if window <= 0 {
		return 1, 1
	}
	if step <= 0 {
		b := autoChartBuckets(window)
		return max(int64(window.Seconds())/int64(b), 1), b
	}
	if step > window {
		step = window
	}
	if n := int(window / step); n > maxChartBuckets {
		step = window / time.Duration(maxChartBuckets)
	}
	stepSec = max(int64(step.Seconds()), 1)
	buckets = max(int(int64(window.Seconds())/stepSec), 1)
	return stepSec, buckets
}
