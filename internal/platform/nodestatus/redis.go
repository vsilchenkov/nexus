// Package nodestatus — Redis-персист исхода последнего исходящего вызова узла
// (§46, §52). Делает runtime-бейдж узла (§41) устойчивым к рестарту/деплою:
// гаудж nexus_node_last_request_error in-memory и теряется при рестарте
// процесса (после деплоя узлы ложно показывают «OK»), а этот ключ в Redis
// переживает рестарт любого из процессов. Sender пишет (RedisWriter), Web читает.
//
// Ключ nexus:node:last_error:<path>; TTL ~30 суток — ключи удалённых/
// простаивающих узлов самоистекают, без неограниченного роста. Best-effort:
// ошибка Redis логируется и не пробрасывается — статус узла не должен влиять
// на доставку (в духе fail-open §9.4 и §41).
//
// Кодировка значения (§52) — НАМЕРЕННО не совпадает с гауджем
// (domain.NodeOutcome.GaugeValue: 0=ok/1=degraded/2=down):
//
//	значение | outcome  | почему
//	---------+----------+---------------------------------------------------
//	"0"      | ok       | как в булевой схеме §46
//	"1"      | down     | legacy: старый Sender писал "1" = «любой не-2xx» —
//	         |          | новый читатель толкует worst case (down); старый
//	         |          | Web (s == "1") при новом Sender продолжает красить
//	         |          | реальный down красным (rolling-совместимость)
//	"2"      | degraded | новое значение; старый Web покажет OK (транзиентно)
//
// Нераспознанное значение читатель пропускает (fallback на Prometheus, как
// отсутствующий ключ). Матрица rolling-комбинаций — в specs/sections/52.
//
// # Down — не «последний вызов упал», а «упало N подряд»
//
// Раньше исход писался как есть: первая же 500 переводила узел в down. На узле
// с постоянным потоком это давало ложную картину — бейдж срывался в down от
// одиночной ошибки среди сотен успешных ответов, а параллельные запросы делали
// его ещё и случайным: статус ставил тот вызов, который ЗАВЕРШИЛСЯ последним,
// а не начался. Пример из боя: два ответа 200 по 35 мс и один 500 за 1493 мс в
// одну секунду — узел показывал down, хотя работал.
//
// Поэтому «тяжёлые» отказы (транспорт или 5xx) копятся в счётчике подряд идущих
// неудач, и down выставляется, только когда счётчик дошёл до порога
// (sender.node_down_threshold). Любой успешный ответ обнуляет счётчик, 4xx его
// не трогает вовсе — это ответ приёмника, а не его отказ.
//
// Счётчик живёт ОТДЕЛЬНЫМ ключом (nexus:node:fail_streak:<path>) с тем же TTL:
// формат значения исхода не меняется, и читатели любой версии продолжают
// работать. Инкремент и решение выполняются одним Lua-скриптом — при двух
// репликах Sender (§93) последовательность «прочитал → посчитал → записал»
// была бы гонкой, теряющей отказы.
package nodestatus

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/domain"
	"nexus/internal/platform/logging"
)

const (
	keyPrefix = "nexus:node:last_error:"
	// streakPrefix — ключ счётчика подряд идущих «тяжёлых» отказов узла.
	streakPrefix = "nexus:node:fail_streak:"
	// defaultTTL — срок жизни ключа исхода. Перезаписывается каждым вызовом узла;
	// длинный TTL нужен лишь чтобы простаивающие/удалённые узлы со временем ушли.
	defaultTTL = 30 * 24 * time.Hour
	// DefaultDownThreshold — сколько «тяжёлых» отказов подряд переводят узел в
	// down. Зеркало дефолта sender.node_down_threshold: writer должен вести себя
	// одинаково и когда порог пришёл из конфига, и когда его забыли передать.
	DefaultDownThreshold = 10
)

// StreakKey — Redis-ключ счётчика подряд идущих отказов узла. Экспортно ради
// диагностики: «почему узел до сих пор не down» отвечается одним GET.
func StreakKey(nodePath string) string { return streakPrefix + nodePath }

// Key возвращает Redis-ключ исхода последнего вызова узла по его пути. Экспортно,
// чтобы читающая сторона (Web-адаптер) использовала тот же формат без дублирования.
func Key(nodePath string) string { return keyPrefix + nodePath }

// EncodeOutcome кодирует исход в Redis-значение (таблица — в doc пакета).
func EncodeOutcome(o domain.NodeOutcome) string {
	switch o {
	case domain.NodeOutcomeDown:
		return "1"
	case domain.NodeOutcomeDegraded:
		return "2"
	default:
		return "0"
	}
}

// DecodeOutcome разбирает Redis-значение исхода. Второй результат false —
// значение не распознано (мусор/будущая схема): вызывающая сторона пропускает
// узел, срабатывает fallback на Prometheus. Legacy "1" (булев «любой не-2xx»
// от старого Sender) намеренно читается как down — worst case, самоисцелится
// следующим вызовом узла.
func DecodeOutcome(s string) (domain.NodeOutcome, bool) {
	switch s {
	case "0":
		return domain.NodeOutcomeOK, true
	case "1":
		return domain.NodeOutcomeDown, true
	case "2":
		return domain.NodeOutcomeDegraded, true
	default:
		return "", false
	}
}

// Noop — заглушка Writer для случая «нет Redis»: поведение остаётся как в §41
// (только in-memory гаудж, теряется при рестарте). Используется на call-site
// вместо nil, чтобы не плодить nil-проверки.
type Noop struct{}

// SetLastOutcome ничего не пишет и возвращает исход без изменений: без Redis
// счётчик подряд идущих отказов хранить негде, поэтому порог не применяется и
// поведение остаётся прежним (§41, только in-memory гаудж).
func (Noop) SetLastOutcome(_ context.Context, _ string, outcome domain.NodeOutcome) domain.NodeOutcome {
	return outcome
}

// outcomeSrc — атомарная запись исхода со счётчиком подряд идущих отказов.
//
// Скрипт целиком, а не три команды подряд: между GET и SET счётчик успевает
// измениться соседней репликой Sender (§93), и часть отказов терялась бы — узел
// не доходил бы до порога никогда. Скрипт выполняется в Redis неделимо.
//
// KEYS[1] — ключ исхода, KEYS[2] — ключ счётчика.
// ARGV[1] — класс исхода: "ok" | "soft" (4xx) | "hard" (5xx/транспорт).
// ARGV[2] — порог, ARGV[3] — TTL обоих ключей в миллисекундах.
// Возвращает закодированный ЭФФЕКТИВНЫЙ исход ("0"/"1"/"2").
const outcomeSrc = `
local class = ARGV[1]
local threshold = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])

if class == 'ok' then
	redis.call('DEL', KEYS[2])
	redis.call('SET', KEYS[1], '0', 'PX', ttl)
	return '0'
end

if class == 'soft' then
	-- Ответ 4xx — это ответ приёмника, а не его отказ: счётчик не трогаем,
	-- иначе поток клиентских ошибок «уронил» бы живой узел.
	redis.call('SET', KEYS[1], '2', 'PX', ttl)
	return '2'
end

local n = redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[2], ttl)
if n >= threshold then
	redis.call('SET', KEYS[1], '1', 'PX', ttl)
	return '1'
end
redis.call('SET', KEYS[1], '2', 'PX', ttl)
return '2'
`

// RedisWriter — Redis-реализация записи исхода последнего вызова (пишется Sender'ом).
type RedisWriter struct {
	client    *goredis.Client
	threshold int
	// script — поле, а не package-level переменная (CLAUDE.md §4): goredis.Script
	// кэширует SHA после первого EVALSHA, то есть несёт состояние.
	script *goredis.Script
	logger logging.Logger
}

// NewRedisWriter создаёт writer. client должен быть ненулевым: вызывающая сторона
// не конструирует RedisWriter при отсутствии Redis-клиента (использует Noop).
//
// threshold — сколько «тяжёлых» отказов подряд переводят узел в down; значение
// меньше 1 заменяется дефолтом (порог 0 означал бы «down всегда»).
func NewRedisWriter(client *goredis.Client, threshold int, logger logging.Logger) *RedisWriter {
	if threshold < 1 {
		threshold = DefaultDownThreshold
	}
	return &RedisWriter{
		client:    client,
		threshold: threshold,
		script:    goredis.NewScript(outcomeSrc),
		logger:    logger,
	}
}

// outcomeClass — как исход влияет на счётчик подряд идущих отказов.
func outcomeClass(o domain.NodeOutcome) string {
	switch o {
	case domain.NodeOutcomeDown:
		return "hard"
	case domain.NodeOutcomeDegraded:
		return "soft"
	default:
		return "ok"
	}
}

// SetLastOutcome best-effort пишет исход последнего вызова узла в Redis и
// возвращает ЭФФЕКТИВНЫЙ исход с учётом порога: сырой down превращается в
// degraded, пока подряд идущих отказов меньше threshold.
//
// Возвращаемое значение — не украшение: по нему вызывающая сторона выставляет
// Prometheus-гаудж, иначе бейдж (Redis) и алерты (Prometheus) говорили бы про
// один узел разное.
//
// Ошибку Redis логирует Warn и не возвращает — фиксация статуса не должна
// влиять на доставку запроса (fail-open §9.4). В этом случае возвращается
// исходный исход: без счётчика решить о пороге нечем.
func (w *RedisWriter) SetLastOutcome(ctx context.Context, nodePath string, outcome domain.NodeOutcome) domain.NodeOutcome {
	if nodePath == "" {
		return outcome
	}
	res, err := w.script.Run(ctx, w.client,
		[]string{Key(nodePath), StreakKey(nodePath)},
		outcomeClass(outcome), w.threshold, defaultTTL.Milliseconds(),
	).Text()
	if err != nil {
		w.logger.Warn("nodestatus: redis set failed",
			w.logger.Str("node", nodePath), w.logger.Err(err))
		return outcome
	}
	eff, ok := DecodeOutcome(res)
	if !ok {
		// Скрипт возвращает только "0"/"1"/"2"; сюда попадём разве что при
		// правке скрипта — сообщаем и не выдумываем исход.
		w.logger.Warn("nodestatus: unexpected script result",
			w.logger.Str("node", nodePath), w.logger.Str("result", res))
		return outcome
	}
	if eff != outcome {
		// §51.9: переход «сырой down → degraded» неочевиден и объясняет, почему
		// узел не покраснел после ошибки.
		w.logger.Debug("nodestatus: outcome adjusted by fail-streak threshold",
			w.logger.Str("node", nodePath),
			w.logger.Str("raw", string(outcome)),
			w.logger.Str("effective", string(eff)),
			w.logger.Int("threshold", w.threshold))
	}
	return eff
}
