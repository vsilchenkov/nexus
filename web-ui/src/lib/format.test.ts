import { describe, it, expect } from "vitest";

import { fmtSize } from "./format";

// §42-доп: локализованный формат размера тела (units из i18n logs.size_units).
describe("fmtSize", () => {
  const ru = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
  const en = ["B", "KB", "MB", "GB", "TB"];

  it.each([
    [0, "—"],
    [-1, "—"],
    [512, "512 Б"],
    [1023, "1023 Б"],
    [1024, "1.0 КБ"],
    [12_288, "12.0 КБ"],
    [5 * 1024 * 1024, "5.0 МБ"],
  ])("ru: %i → %s", (n, want) => {
    expect(fmtSize(n, ru)).toBe(want);
  });

  it("uses english units for en locale", () => {
    expect(fmtSize(12_288, en)).toBe("12.0 KB");
  });

  it("clamps to the largest unit provided", () => {
    expect(fmtSize(2 ** 60, ru)).toBe(`${(2 ** 60 / 1024 ** 4).toFixed(1)} ТБ`);
  });

  it("returns dash when units are missing", () => {
    expect(fmtSize(100, [])).toBe("—");
  });
});
