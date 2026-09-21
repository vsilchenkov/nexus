import { describe, expect, it } from "vitest";

import { commentOnlyHit, commentSnippet, containsFolded, SNIPPET_CONTEXT } from "./commentSnippet";

const pad = (n: number, ch = "a") => ch.repeat(n);
// Одиночная (непарная) суррогатная единица — признак разрезанного emoji.
const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/;

describe("commentSnippet (§103.4)", () => {
  it.each([
    {
      name: "совпадение в середине — «…» с двух сторон",
      comment: pad(50, "x") + " СДЭК " + pad(50, "y"),
      q: "сдэк",
      want: {
        before: "…" + pad(SNIPPET_CONTEXT - 1, "x") + " ",
        match: "СДЭК",
        after: " " + pad(SNIPPET_CONTEXT - 1, "y") + "…",
      },
    },
    {
      name: "совпадение в начале — многоточие только справа",
      comment: "СДЭК, " + pad(60, "z"),
      q: "сдэк",
      want: { before: "", match: "СДЭК", after: ", " + pad(SNIPPET_CONTEXT - 2, "z") + "…" },
    },
    {
      name: "совпадение в конце — многоточие только слева",
      comment: pad(60, "z") + " сдэк",
      q: "СДЭК",
      want: { before: "…" + pad(SNIPPET_CONTEXT - 1, "z") + " ", match: "сдэк", after: "" },
    },
    {
      name: "короткий комментарий — целиком, без многоточий",
      comment: "Интеграция со СДЭК",
      q: "сдэк",
      want: { before: "Интеграция со ", match: "СДЭК", after: "" },
    },
    {
      name: "переводы строк и табы схлопываются в пробел",
      comment: "строка раз\n\nСДЭК\tтикет",
      q: "сдэк",
      want: { before: "строка раз ", match: "СДЭК", after: " тикет" },
    },
    {
      name: "первое из нескольких совпадений",
      comment: "сдэк один, СДЭК два",
      q: "Сдэк",
      want: { before: "", match: "сдэк", after: " один, СДЭК два" },
    },
    {
      // Наивный indexOf по comment.toLowerCase() сдвинулся бы на 3 единицы:
      // «İ» при свёртке становится двумя единицами UTF-16.
      name: "свёртка меняет длину строки — индексы не съезжают",
      comment: "İİİ сдэк",
      q: "сдэк",
      want: { before: "İİİ ", match: "сдэк", after: "" },
    },
  ])("$name", ({ comment, q, want }) => {
    expect(commentSnippet(comment, q)).toEqual(want);
  });

  it.each([
    { name: "нет совпадения", comment: "Интеграция с DPD", q: "сдэк" },
    { name: "пустой запрос", comment: "Интеграция", q: "" },
    { name: "пустой комментарий", comment: "", q: "сдэк" },
    // Посимвольный lower() PG: «Σ» → «σ», а не контекстная «ς». ILIKE не
    // найдёт «οδος» в «ΟΔΟΣ» — и сниппет не должен.
    { name: "финальная сигма — как посимвольный lower() PG", comment: "ΟΔΟΣ", q: "οδος" },
  ])("$name → null", ({ comment, q }) => {
    expect(commentSnippet(comment, q)).toBeNull();
  });

  it("emoji на границе окна не разрезается", () => {
    const s = commentSnippet("😀".repeat(50) + "сдэк" + "🚚".repeat(50), "сдэк")!;
    expect(s.match).toBe("сдэк");
    expect(s.before).toBe("…" + "😀".repeat(SNIPPET_CONTEXT));
    expect(s.after).toBe("🚚".repeat(SNIPPET_CONTEXT) + "…");
    expect(s.before + s.after).not.toMatch(LONE_SURROGATE);
  });
});

describe("containsFolded", () => {
  it.each([
    { text: "svc/CDEK-orders", q: "cdek", want: true },
    { text: "Логистика", q: "ЛОГ", want: true },
    { text: "svc/orders", q: "cdek", want: false },
    { text: "svc/orders", q: "", want: false },
  ])("$text ∋ $q → $want", ({ text, q, want }) => {
    expect(containsFolded(text, q)).toBe(want);
  });
});

describe("commentOnlyHit (§103.4)", () => {
  const node = {
    path: "svc/orders",
    target_url: "https://api.example.com/orders",
    comment: "Интеграция со СДЭК",
  };

  it("совпал только комментарий → сниппет", () => {
    expect(commentOnlyHit(node, "сдэк")).toEqual({
      before: "Интеграция со ",
      match: "СДЭК",
      after: "",
    });
  });

  it.each([
    { name: "совпадение в пути", q: "ORDERS" },
    { name: "совпадение в адресе", q: "api.example" },
  ])("$name → null (подсказка лишняя)", ({ q }) => {
    expect(commentOnlyHit(node, q)).toBeNull();
  });

  it("без комментария или без запроса → null", () => {
    expect(commentOnlyHit({ path: "a" }, "сдэк")).toBeNull();
    expect(commentOnlyHit(node, "")).toBeNull();
  });
});
