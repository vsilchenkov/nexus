import { describe, expect, it } from "vitest";

import {
  CHART_STEP_LADDER,
  defaultStepFor,
  isChartStep,
  normalizeStepForPeriod,
  periodMs,
  isStepTooFine,
  stepsForPeriod,
  type Period,
} from "./period";

// §84.1/§84.3: словарь шага графика, его сужение по периоду и правило дефолта.
// До §84 словарь совпадал с пресетами периода: минимальным шагом был ЧАС, и на
// окне 1 ч выбрать было нечего вовсе, а дефолтом всегда шёл «Авто».

type Preset = "1h" | "3h" | "24h" | "7d" | "14d" | "30d";
const PRESETS: Preset[] = ["1h", "3h", "24h", "7d", "14d", "30d"];
const preset = (range: Preset): Period => ({ kind: "preset", range });

// STEP_MS — длительности ступеней; нужны, чтобы считать столбцы в тесте своим
// кодом, а не переиспользовать внутреннюю таблицу проверяемого модуля.
const STEP_MS: Record<string, number> = {
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

describe("stepsForPeriod (§84.1)", () => {
  it("«Авто» есть всегда и идёт первым", () => {
    for (const r of PRESETS) {
      expect(stepsForPeriod(preset(r))[0]).toBe("auto");
    }
  });

  it("на часе доступна минута — до §84 минимальным шагом был час", () => {
    expect(stepsForPeriod(preset("1h"))).toContain("1m");
  });

  // Сверху НЕ отсекаем: шаг крупнее окна — определённое поведение §79.5
  // (сервер сжимает его до окна и отдаёт один столбец), а не ошибка. Раньше
  // такие ступени прятались, и 7д/14д/30д пропадали из списка на коротких
  // периодах — выглядело как будто их нет вовсе.
  it("крупные ступени есть на ЛЮБОМ периоде — красный на прежнем правиле", () => {
    for (const r of PRESETS) {
      const steps = stepsForPeriod(preset(r));
      expect(steps).toContain("7d");
      expect(steps).toContain("14d");
      expect(steps).toContain("30d");
    }
  });

  it("убранные из пикера ступени в словаре не значатся", () => {
    const all = stepsForPeriod(preset("30d")).concat(stepsForPeriod(preset("1h")));
    for (const gone of ["5m", "10m", "2h"]) expect(all).not.toContain(gone);
  });

  // Набор ПОСТОЯНЕН: пока он менялся вместе с периодом, менялась ширина строки
  // заголовка и вся шапка вкладки прыгала при переключении периода.
  it("набор кнопок одинаков на любом периоде — иначе шапка прыгает", () => {
    const ref = stepsForPeriod(preset("24h"));
    for (const r of PRESETS) expect(stepsForPeriod(preset(r))).toEqual(ref);
    expect(ref).toEqual(["auto", ...CHART_STEP_LADDER]);
  });

  it("битый произвольный период не роняет расчёт", () => {
    const broken: Period = { kind: "custom", from: "мусор", to: "тоже мусор" };
    expect(periodMs(broken)).toBe(0);
    expect(stepsForPeriod(broken)[0]).toBe("auto");
  });

});

// Применимость выражается ГАШЕНИЕМ, а не исчезновением кнопки.
describe("isStepTooFine (§84.1)", () => {
  it("на 30 сутках минута и полчаса гасятся: сервер поднял бы их до потолка", () => {
    // 30 суток / 400 = 6480 с, значит всё мельче 1 ч 48 мин неприменимо.
    expect(isStepTooFine("1m", preset("30d"))).toBe(true);
    expect(isStepTooFine("30m", preset("30d"))).toBe(true);
    expect(isStepTooFine("3h", preset("30d"))).toBe(false);
  });

  it("крупные ступени не гасятся НИКОГДА — шаг больше окна даёт один столбец", () => {
    for (const r of PRESETS) {
      expect(isStepTooFine("30d", preset(r))).toBe(false);
      expect(isStepTooFine("7d", preset(r))).toBe(false);
    }
  });

  it("«Авто» не гасится и на вырожденном окне ничего не гасится", () => {
    expect(isStepTooFine("auto", preset("30d"))).toBe(false);
    const broken: Period = { kind: "custom", from: "мусор", to: "тоже мусор" };
    expect(isStepTooFine("1m", broken)).toBe(false);
  });

  it("дефолт периода никогда не погашен", () => {
    for (const r of PRESETS) {
      const p = preset(r);
      expect(isStepTooFine(defaultStepFor(p), p)).toBe(false);
    }
  });
});

describe("defaultStepFor (§84.3)", () => {
  // Таблица из ТЗ §84.3. Дефолт перестал быть «Авто»: подсвечен конкретный
  // сегмент, и он даёт 24–36 столбцов на любом пресете.
  it.each([
    ["1h", "1m", 60],
    // На трёх часах промежуточной ступени не осталось: 15м дают всего 12
    // столбцов (меньше порога), поэтому дефолт — минута. Пятнадцать минут
    // по-прежнему доступны выбором.
    ["3h", "1m", 180],
    ["24h", "1h", 24],
    ["7d", "6h", 28],
    ["14d", "12h", 28],
    ["30d", "24h", 30],
  ] as const)("период %s → шаг %s, столбцов %i", (range, want, buckets) => {
    const p = preset(range);
    expect(defaultStepFor(p)).toBe(want);
    // Число столбцов считаем сами: константа в таблице обязана быть следствием
    // правила, а не просто переписанной из реализации.
    expect(periodMs(p) / STEP_MS[want]).toBe(buckets);
  });

  it("дефолт даёт не меньше 24 столбцов на любом пресете — это и есть правило", () => {
    for (const r of PRESETS) {
      const p = preset(r);
      const s = defaultStepFor(p);
      expect(periodMs(p) / STEP_MS[s]).toBeGreaterThanOrEqual(24);
    }
  });

  it("более крупная ступень дала бы меньше 24 столбцов — дефолт максимален", () => {
    for (const r of PRESETS) {
      const p = preset(r);
      const idx = CHART_STEP_LADDER.indexOf(defaultStepFor(p) as (typeof CHART_STEP_LADDER)[number]);
      const bigger = CHART_STEP_LADDER[idx + 1];
      if (bigger) expect(periodMs(p) / STEP_MS[bigger]).toBeLessThan(24);
    }
  });

  it("дефолт всегда входит в словарь", () => {
    for (const r of PRESETS) {
      const p = preset(r);
      expect(stepsForPeriod(p)).toContain(defaultStepFor(p));
    }
  });

  it("дефолт никогда не «Авто» на осмысленном окне", () => {
    for (const r of PRESETS) {
      expect(defaultStepFor(preset(r))).not.toBe("auto");
    }
  });
});

describe("normalizeStepForPeriod (§84.3)", () => {
  it("шаг, не помещающийся в новый период, заменяется дефолтом нового периода", () => {
    // «30м» на окне 30 суток дал бы 1440 столбцов — сервер поднял бы шаг, и
    // подсвеченный сегмент врал бы о фактической ширине столбца.
    expect(normalizeStepForPeriod("30m", preset("30d"))).toBe("24h");
  });

  it("подходящий шаг сохраняется", () => {
    expect(normalizeStepForPeriod("6h", preset("7d"))).toBe("6h");
  });

  it("«Авто» переживает смену периода — это осознанный выбор, а не значение", () => {
    expect(normalizeStepForPeriod("auto", preset("30d"))).toBe("auto");
    expect(normalizeStepForPeriod("auto", preset("1h"))).toBe("auto");
  });
});

describe("isChartStep", () => {
  it("распознаёт всю лестницу и «Авто»", () => {
    expect(isChartStep("auto")).toBe(true);
    for (const s of CHART_STEP_LADDER) expect(isChartStep(s)).toBe(true);
  });

  it("мусор и убранные из пикера значения не проходят", () => {
    expect(isChartStep("5m")).toBe(false); // убран по требованию (слишком много кнопок)
    expect(isChartStep("2h")).toBe(false);
    expect(isChartStep("zzz")).toBe(false);
    expect(isChartStep(null)).toBe(false);
    expect(isChartStep(undefined)).toBe(false);
  });
});
