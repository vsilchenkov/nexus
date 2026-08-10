import { describe, expect, it } from "vitest";

import { capacityShare, capacityTone, fmtCapacityDuration, fmtShare } from "./queueCapacity";

// §84.8 (узловой срез §80.2): помещается ли узел в одну партицию Kafka.
// Эталон — боевой замер §80.1, ради которого раздел и написан.

const HOUR = 3600_000;

describe("capacityShare (§84.8)", () => {
  // §80.1, замер 05.08.2026: acs_sigur — 219 запросов/ч при p95 22 366 мс, то
  // есть 136 % ёмкости партиции. Он же блокировал omnichannel (2,7 %),
  // захешированный на ту же партицию.
  it("боевой acs_sigur: 219 × 22 366 мс / 1 ч ≈ 136 %", () => {
    const s = capacityShare({ total: 219, p95Ms: 22366, rangeMs: HOUR });
    expect(s).not.toBeNull();
    expect(Math.round(s! * 100)).toBe(136);
  });

  it("боевой omnichannel: 1138 × 86 мс / 1 ч ≈ 2,7 %", () => {
    const s = capacityShare({ total: 1138, p95Ms: 86, rangeMs: HOUR });
    expect((s! * 100).toFixed(1)).toBe("2.7");
  });

  // null, а НЕ ноль: ноль читался бы как «узел ничего не занимает», а мы
  // просто не знаем.
  it.each([
    ["нет запросов", { total: 0, p95Ms: 100, rangeMs: HOUR }],
    ["нет окна", { total: 10, p95Ms: 100, rangeMs: 0 }],
    ["нет p95", { total: 10, p95Ms: 0, rangeMs: HOUR }],
    ["NaN на входе", { total: NaN, p95Ms: 100, rangeMs: HOUR }],
  ])("%s → null, а не ноль", (_name, args) => {
    expect(capacityShare(args)).toBeNull();
  });
});

describe("capacityTone", () => {
  it("выше 100 % — ошибка, а не предупреждение: узел структурно не помещается", () => {
    expect(capacityTone(1.36)).toBe("err");
    expect(capacityTone(1)).toBe("err");
  });

  it("на подходе к потолку — предупреждение", () => {
    expect(capacityTone(0.8)).toBe("warn");
  });

  it("штатная загрузка и «не знаем» тона не требуют", () => {
    expect(capacityTone(0.027)).toBe("ok");
    expect(capacityTone(null)).toBe("ok");
  });
});

describe("fmtShare", () => {
  it("ниже 10 % значим знак после запятой", () => {
    expect(fmtShare(0.027)).toBe("2.7 %");
  });

  it("выше 10 % дробь только мешает", () => {
    expect(fmtShare(1.361)).toBe("136 %");
  });
});

// Найдено прогоном на стенде: пояснение к формуле печатало «p95 0.0 с» при
// p95 = 2 мс. Строка, объясняющая расчёт, читалась как «p95 нулевая».
describe("fmtCapacityDuration (§84.8)", () => {
  const U = ["мс", "с", "мин", "ч"];

  it("быстрый узел измеряется миллисекундами, а не «0.0 с»", () => {
    expect(fmtCapacityDuration(2, U)).toBe("2 мс");
    expect(fmtCapacityDuration(999, U)).toBe("999 мс");
  });

  it("короткое окно измеряется минутами, а не «0 ч»", () => {
    expect(fmtCapacityDuration(30 * 60_000, U)).toBe("30 мин");
  });

  it("боевые величины §80.1 не меняются", () => {
    expect(fmtCapacityDuration(22366, U)).toBe("22.4 с");
    expect(fmtCapacityDuration(HOUR, U)).toBe("1 ч");
    expect(fmtCapacityDuration(24 * HOUR, U)).toBe("24 ч");
  });

  it("нечисло и ноль не печатают «NaN»", () => {
    expect(fmtCapacityDuration(NaN, U)).toBe("0 мс");
    expect(fmtCapacityDuration(0, U)).toBe("0 мс");
  });
});
