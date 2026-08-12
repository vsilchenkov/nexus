import { describe, expect, it } from "vitest";

import { type Role, roleAtLeast, roleRank } from "./roles";

// §26/§87: иерархия ролей — единственный источник правды для всех UI-гейтов, и
// до §87 файл не был покрыт вовсе. Тест фиксирует ПОРЯДОК, а не конкретные
// числа: ранги вычисляемые и могут сдвигаться при вставке новой роли (так
// `operator` и появился между viewer и manager), а вот отношение «кто кого
// выше» ломать нельзя — на нём висят и фронт, и бэкенд.

const ORDER: Role[] = ["viewer", "operator", "manager", "admin"];

describe("roleRank", () => {
  it("выстраивает роли строго по возрастанию прав", () => {
    const ranks = ORDER.map(roleRank);
    expect(ranks).toEqual([...ranks].sort((a, b) => a - b));
    expect(new Set(ranks).size).toBe(ORDER.length);
  });

  it("неизвестную роль и отсутствие роли трактует как минимум", () => {
    expect(roleRank(undefined)).toBe(roleRank("viewer"));
    expect(roleRank("editor")).toBe(roleRank("viewer"));
    // Регистр значим — бэкенд отдаёт роль в нижнем регистре.
    expect(roleRank("Admin")).toBe(roleRank("viewer"));
  });
});

describe("roleAtLeast", () => {
  it.each(
    ORDER.flatMap((role, i) =>
      ORDER.map((min, j) => ({ role, min, want: i >= j })),
    ),
  )("$role vs $min → $want", ({ role, min, want }) => {
    expect(roleAtLeast(role, min)).toBe(want);
  });

  it("оператор проходит свои гейты, но не менеджерские (§87)", () => {
    expect(roleAtLeast("operator", "operator")).toBe(true);
    expect(roleAtLeast("operator", "viewer")).toBe(true);
    expect(roleAtLeast("operator", "manager")).toBe(false);
    expect(roleAtLeast("operator", "admin")).toBe(false);
  });

  it("менеджер и админ наследуют операторские права", () => {
    expect(roleAtLeast("manager", "operator")).toBe(true);
    expect(roleAtLeast("admin", "operator")).toBe(true);
  });

  it("наблюдателю операторские действия закрыты", () => {
    expect(roleAtLeast("viewer", "operator")).toBe(false);
    expect(roleAtLeast(undefined, "operator")).toBe(false);
  });
});
