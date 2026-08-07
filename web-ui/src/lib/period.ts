// Период просмотра метрик (§28, Пункт 4): пресет 1h..30d ИЛИ произвольный
// календарный диапазон from/to (RFC3339). Вынесено из PeriodPicker, чтобы
// не ломать react-refresh (файл компонента экспортирует только компонент).
export type PresetRange = "1h" | "3h" | "24h" | "7d" | "14d" | "30d";
export type Period =
  | { kind: "preset"; range: PresetRange }
  | { kind: "custom"; from: string; to: string };

export const PRESET_RANGES: PresetRange[] = ["1h", "3h", "24h", "7d", "14d", "30d"];

// defaultPeriod — системный дефолт периода метрик (§44.B): 24ч (раньше 1ч).
// На рабочем столе перекрывается персональным дефолтом команды (§71,
// lib/prefs.ts: преф команды → глобальный преф → это значение). Вкладки узла и
// Kafka-монитор используют его как есть — они вне scope §71.
export const defaultPeriod: Period = { kind: "preset", range: "24h" };

// parsePeriodPref — Period из произвольного значения: приходит с сервера
// (преф §71) или из прежнего localStorage-ключа (миграция §71.5). Не пресет —
// null: дефолтом сохраняется только пресет, произвольный календарный диапазон
// в этой роли смысла не имеет. Значение сервер не валидирует (§71.3), контракт
// держит эта функция.
export function parsePeriodPref(raw: unknown): Period | null {
  if (!raw || typeof raw !== "object") return null;
  const p = raw as Partial<Period>;
  if (p.kind !== "preset") return null;
  const range = (p as { range?: unknown }).range;
  if (typeof range !== "string" || !(PRESET_RANGES as string[]).includes(range)) return null;
  return { kind: "preset", range: range as PresetRange };
}

// periodParams — query-параметры для /api/metrics/* по выбранному периоду.
export function periodParams(p: Period): Record<string, string> {
  return p.kind === "preset" ? { range: p.range } : { from: p.from, to: p.to };
}

// §79.5 «Шаг графика» — ширина ОДНОГО столбца. Не путать с периодом: период —
// сколько показываем, шаг — насколько крупными столбцами. Пара «период 14д +
// шаг 24ч» даёт 14 столбцов, по одному на сутки.
//
// §84.1: словарь шага для ВЫБОРА. До §84 он совпадал с пресетами периода, то
// есть минимальным доступным шагом был ЧАС, и на окне 1 ч выбрать было нечего
// вовсе; теперь есть и минута, и крупные ступени до 30 суток.
//
// Это словарь ПИКЕРА, и он НЕ совпадает с лестницей авто-снапа на сервере
// (usecase/metrics_window.go, chartStepLadder) — намеренно. Серверная лестница
// плотнее (в ней есть 5м/10м/2ч), потому что «Авто» подбирает ширину под
// целевую плотность и промежуточные ступени там нужны. В интерфейсе они
// оказались лишними: десять кнопок читаются хуже, чем шесть, а выбрать между
// «5м» и «10м» на глаз всё равно невозможно. Значения, общие для обоих
// списков, совпадают — расхождение только в наборе.
//
// Все значения до суток делят сутки нацело (выравнивание столбцов), крупные —
// кратны суткам.
//
// Бэкенд менять не требуется: ParseChartStep принимает Go-длительности
// (`15m`, `6h`, `12h`) помимо пресетов, а `1h`/`3h`/`24h`/`7d`/`14d`/`30d`
// есть в его словаре.
export const CHART_STEP_LADDER = [
  "1m",
  "15m",
  "30m",
  "1h",
  "3h",
  "6h",
  "12h",
  "24h",
  "7d",
  "14d",
  "30d",
] as const;

export type ChartStepValue = (typeof CHART_STEP_LADDER)[number];
export type ChartStep = "auto" | ChartStepValue;

export const CHART_STEPS: ChartStep[] = ["auto", ...CHART_STEP_LADDER];

const CHART_STEP_MS: Record<ChartStepValue, number> = {
  "1m": 60_000,
  "15m": 900_000,
  "30m": 1_800_000,
  "1h": 3_600_000,
  "3h": 10_800_000,
  "6h": 21_600_000,
  "12h": 43_200_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
  "14d": 1_209_600_000,
  "30d": 2_592_000_000,
};

// MAX_CHART_BUCKETS — зеркало maxChartBuckets сервера. Шаг мельче, чем
// окно/400, сервер всё равно поднимет до потолка, поэтому предлагать его в
// интерфейсе значит обещать плотность, которой не будет.
const MAX_CHART_BUCKETS = 400;

// MIN_DEFAULT_BUCKETS — сколько столбцов должен давать шаг по умолчанию
// (§84.3). Ниже графику нечего показывать, выше — дефолт становится мельче
// прежнего «Авто» и дороже для ClickHouse.
const MIN_DEFAULT_BUCKETS = 24;

/** isChartStep — распознавание значения шага (адрес, преф — источники внешние). */
export function isChartStep(raw: unknown): raw is ChartStep {
  return raw === "auto" || (CHART_STEP_LADDER as readonly string[]).includes(raw as string);
}

// periodMs — длительность окна периода в миллисекундах.
export function periodMs(p: Period): number {
  if (p.kind === "preset") return PRESET_MS[p.range];
  const w = periodWindow(p);
  return Number.isFinite(w.since) && Number.isFinite(w.until) ? Math.max(w.until - w.since, 0) : 0;
}

/**
 * stepsForPeriod — какие шаги предлагать при этом периоде (§84.1).
 *
 * Отсекается ТОЛЬКО снизу: шаг мельче `окно / 400` сервер молча поднимет до
 * потолка, то есть сегмент выглядел бы рабочим, а действовал иначе, чем
 * написано. Это единственный случай, когда кнопка врёт.
 *
 * Сверху не отсекается. Шаг крупнее окна — не ошибка, а определённое поведение
 * §79.5: сервер сжимает его до окна и отдаёт ровно один столбец («одна цифра за
 * весь период»). Раньше такие ступени прятались, и крупные шаги (7д/14д/30д)
 * пропадали из списка на коротких периодах — выглядело это так, будто их нет
 * вовсе.
 *
 * «Авто» есть всегда. На вырожденном окне останется только он.
 */
export function stepsForPeriod(p: Period): ChartStep[] {
  const window = periodMs(p);
  if (window <= 0) return ["auto"];
  const min = window / MAX_CHART_BUCKETS;
  return ["auto", ...CHART_STEP_LADDER.filter((s) => CHART_STEP_MS[s] >= min)];
}

/**
 * defaultStepFor — шаг по умолчанию для периода (§84.3).
 *
 * Правило: НАИБОЛЬШАЯ ступень, дающая не меньше MIN_DEFAULT_BUCKETS столбцов.
 * Даёт 1ч→1м, 3ч→5м, 24ч→1ч, 7д→6ч, 14д→12ч, 30д→1сут — везде 24–36 столбцов.
 *
 * Дефолт — конкретное значение, а не «Авто»: подсвеченный сегмент сразу
 * сообщает масштаб, в котором пользователь смотрит, и его видно куда сдвинуть.
 * «Авто» остаётся в словаре как отдельный выбор.
 */
export function defaultStepFor(p: Period): ChartStep {
  const window = periodMs(p);
  if (window <= 0) return "auto";
  const fits = CHART_STEP_LADDER.filter((s) => window / CHART_STEP_MS[s] >= MIN_DEFAULT_BUCKETS);
  if (fits.length === 0) return CHART_STEP_LADDER[0];
  return fits[fits.length - 1];
}

/**
 * normalizeStepForPeriod — шаг, применимый к ЭТОМУ периоду.
 *
 * Нужен при смене периода: сохранённый «30м» на окне 30 суток дал бы 1440
 * столбцов, сервер поднял бы шаг до потолка, и подсвеченный сегмент врал бы о
 * фактической ширине столбца. Неподходящий шаг заменяется дефолтом НОВОГО
 * периода, а не молча оставляется.
 */
export function normalizeStepForPeriod(s: ChartStep, p: Period): ChartStep {
  return stepsForPeriod(p).includes(s) ? s : defaultStepFor(p);
}

/** stepParams — query-параметр шага; auto не отправляем (сервер выберет сам). */
export function stepParams(s: ChartStep): Record<string, string> {
  return s === "auto" ? {} : { step: s };
}

/** stepKey — стабильная часть ключа react-query. */
export function stepKey(s: ChartStep): string {
  return `s:${s}`;
}

// PRESET_MS — длительность пресета в миллисекундах.
const PRESET_MS: Record<PresetRange, number> = {
  "1h": 3_600_000,
  "3h": 10_800_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
  "14d": 1_209_600_000,
  "30d": 2_592_000_000,
};

// periodWindow — границы окна [since, until] в мс (для расчёта временных
// бакетов спарклайна). Пресет: until = «сейчас» (бакеты считаются на сервере
// в момент запроса, клиентский дрейф в пределах секунд — для тултипа ок).
// Произвольный период: явные from/to.
export function periodWindow(p: Period): { since: number; until: number } {
  if (p.kind === "custom") {
    return { since: Date.parse(p.from), until: Date.parse(p.to) };
  }
  const until = Date.now();
  return { since: until - PRESET_MS[p.range], until };
}

// periodKey — стабильный ключ периода для queryKey TanStack Query.
export function periodKey(p: Period): string {
  return p.kind === "preset" ? `r:${p.range}` : `c:${p.from}:${p.to}`;
}

// periodLabel — короткая метка периода для заголовков (§28 Пункт 4): пресет →
// локализованное "1ч"/"24ч"/…, произвольный → "Произвольный". t — i18n-функция.
export function periodLabel(p: Period, t: (k: string) => string): string {
  return p.kind === "preset" ? t(`metrics.range.${p.range}`) : t("metrics.range.custom");
}

/**
 * resolveCustomPeriod — фактическое окно из полей «от»/«по» (§84.4).
 *
 * Открытые границы разрешаются ЗДЕСЬ, в момент выбора, а не размазываются по
 * контракту `Period`: наружу уходит всегда конкретный диапазон. Благодаря
 * этому не меняется ни один потребитель периода, а нижняя граница всегда
 * материализована — сужение по колонке партиционирования (§72.4) остаётся
 * единственным, что держит стоимость запроса на боевых таблицах.
 *
 *   пустое «по» → сейчас;
 *   пустое «от» → «по» минус глубина хранения (старше данных нет физически);
 *   обе пустые  → null: один клик по «Произвольный» не имеет права запускать
 *                 полный скан таблицы.
 */
export function resolveCustomPeriod(
  fromLocal: string,
  toLocal: string,
  maxLookbackMs: number,
  now: number = Date.now(),
): { from: string; to: string } | null {
  if (!fromLocal && !toLocal) return null;
  const toMs = toLocal ? Date.parse(toLocal) : now;
  if (!Number.isFinite(toMs)) return null;
  const fromMs = fromLocal ? Date.parse(fromLocal) : toMs - maxLookbackMs;
  if (!Number.isFinite(fromMs) || fromMs >= toMs) return null;
  return { from: new Date(fromMs).toISOString(), to: new Date(toMs).toISOString() };
}
