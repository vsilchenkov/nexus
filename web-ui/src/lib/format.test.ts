import { describe, it, expect } from "vitest";

import { fmtSize, middleEllipsis } from "./format";

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

// §102.5: сокращение имени метода В СЕРЕДИНЕ. Хвост различает методы одного
// узла, поэтому обрезать конец нельзя.
describe("middleEllipsis", () => {
  it("возвращает строку как есть, когда она влезает", () => {
    expect(middleEllipsis("api/v1/ping", 20)).toBe("api/v1/ping");
    expect(middleEllipsis("api/v1/ping", 11)).toBe("api/v1/ping");
  });

  it("укладывается ровно в бюджет символов", () => {
    const long = "suggestions/api/4_1/rs/suggest/address";
    for (const max of [8, 12, 20, 33]) {
      expect(Array.from(middleEllipsis(long, max))).toHaveLength(max);
    }
  });

  it("сохраняет начало и хвост, вырезая середину", () => {
    const out = middleEllipsis("suggestions/api/4_1/rs/suggest/address", 20);
    expect(out).toBe("suggestio…st/address");
    expect(out.startsWith("suggestio")).toBe(true);
    expect(out.endsWith("/address")).toBe(true);
  });

  it("различает методы, отличающиеся только хвостом", () => {
    const a = middleEllipsis("api/4_1/rs/findById/address", 18);
    const b = middleEllipsis("api/4_1/rs/findById/party", 18);
    expect(a).not.toBe(b);
  });

  it("отдаёт лишний символ хвосту при нечётном остатке", () => {
    // бюджет 6 = многоточие + 2 символа начала + 3 хвоста
    expect(middleEllipsis("abcdefghij", 6)).toBe("ab…hij");
  });

  it("не режет суррогатные пары пополам", () => {
    const s = "🚀🚀🚀🚀🚀🚀";
    const out = middleEllipsis(s, 3);
    expect(Array.from(out)).toEqual(["🚀", "…", "🚀"]);
  });

  it("вырожденные бюджеты", () => {
    expect(middleEllipsis("abcdef", 1)).toBe("…");
    expect(middleEllipsis("abcdef", 0)).toBe("");
    expect(middleEllipsis("", 10)).toBe("");
  });
});
