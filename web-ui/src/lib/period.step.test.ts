import { describe, expect, it } from "vitest";

import {
  CHART_STEP_LADDER,
  defaultStepFor,
  isChartStep,
  normalizeStepForPeriod,
  periodMs,
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
  "5m": 300_000,
  "10m": 600_000,
  "15m": 900_000,
  "30m": 1_800_000,
  "1h": 3_600_000,
  "2h": 7_200_000,
  "3h": 10_800_000,
  "6h": 21_600_000,
  "12h": 43_200_000,
  "24h": 86_400_000,
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

  it("шаг крупнее окна не предлагается: сервер сжал бы его до окна (один столбец)", () => {
    expect(stepsForPeriod(preset("1h"))).not.toContain("24h");
    expect(stepsForPeriod(preset("3h"))).not.toContain("6h");
  });

  it("шаг мельче окна/400 не предлагается: сервер поднял бы его до потолка", () => {
    // 30 суток / 400 = 6480 с, значит всё мельче 2 ч отпадает.
    const steps = stepsForPeriod(preset("30d"));
    expect(steps).not.toContain("1m");
    expect(steps).not.toContain("30m");
    expect(steps).toContain("24h");
  });

  it("вырожденное окно оставляет только «Авто», а не заведомо сжимаемый шаг", () => {
    const tiny: Period = { kind: "custom", from: "2026-08-07T10:00:00.000Z", to: "2026-08-07T10:00:20.000Z" };
    expect(stepsForPeriod(tiny)).toEqual(["auto"]);
  });

  it("битый произвольный период не роняет расчёт", () => {
    const broken: Period = { kind: "custom", from: "мусор", to: "тоже мусор" };
    expect(periodMs(broken)).toBe(0);
    expect(stepsForPeriod(broken)).toEqual(["auto"]);
  });
});

describe("defaultStepFor (§84.3)", () => {
  // Таблица из ТЗ §84.3. Дефолт перестал быть «Авто»: подсвечен конкретный
  // сегмент, и он даёт 24–36 столбцов на любом пресете.
  it.each([
    ["1h", "1m", 60],
    ["3h", "5m", 36],
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

  it("дефолт всегда входит в словарь, доступный для этого периода", () => {
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

  it("мусор и старые значения не проходят", () => {
    expect(isChartStep("7d")).toBe(false); // был в словаре до §84.1
    expect(isChartStep("zzz")).toBe(false);
    expect(isChartStep(null)).toBe(false);
    expect(isChartStep(undefined)).toBe(false);
  });
});
