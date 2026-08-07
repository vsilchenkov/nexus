import { describe, expect, it } from "vitest";

import { capacityShare, capacityTone, fmtShare } from "./queueCapacity";

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
