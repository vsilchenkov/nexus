import { beforeEach, describe, expect, it } from "vitest";

import {
  groupNodes,
  hasGroupedSections,
  hasUngroupedNodes,
  loadCollapsed,
  saveCollapsed,
  usedGroups,
} from "./nodeGroups";
import { type Node, type NodeGroup } from "../api/client";

function node(id: string, groupID?: string): Node {
  return { id, path: id, group_id: groupID } as unknown as Node;
}

function grp(id: string, name: string, order: number): NodeGroup {
  return {
    id,
    name,
    description: "",
    sort_order: order,
    usage_count: 0,
    created_by: "",
    updated_by: "",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
  };
}

// Справочник приходит уже отсортированным сервером (sort_order, name).
const GROUPS = [grp("g1", "1С Обмен", 10), grp("g2", "Курьерские", 20), grp("g3", "МП", 30)];

describe("groupNodes", () => {
  it("узлы без группы идут первой секцией и без группы в заголовке", () => {
    const sections = groupNodes([node("a"), node("b", "g1")], GROUPS);
    expect(sections).toHaveLength(2);
    expect(sections[0].group).toBeNull();
    expect(sections[0].nodes.map((n) => n.id)).toEqual(["a"]);
    expect(sections[1].group?.id).toBe("g1");
  });

  // Ключевое свойство: порядок внутри секции — это порядок входного массива.
  // На нём держится «проблемные первыми внутри своей группы» (§22.5 + §99.5).
  it("сохраняет взаимный порядок узлов внутри секции", () => {
    const input = [node("degraded", "g1"), node("ok", "g1"), node("disabled", "g1")];
    const [section] = groupNodes(input, GROUPS);
    expect(section.nodes.map((n) => n.id)).toEqual(["degraded", "ok", "disabled"]);
  });

  it("секции идут в порядке справочника, а не появления узлов", () => {
    // Узлы намеренно идут «третья группа, первая, вторая».
    const input = [node("c", "g3"), node("a", "g1"), node("b", "g2")];
    const sections = groupNodes(input, GROUPS);
    expect(sections.map((s) => s.group?.id)).toEqual(["g1", "g2", "g3"]);
  });

  it("пустые группы не выводятся", () => {
    const sections = groupNodes([node("a", "g2")], GROUPS);
    expect(sections.map((s) => s.group?.id)).toEqual(["g2"]);
  });

  // Гонка: справочник ещё не приехал или группу удалили в соседней вкладке.
  // Узел важнее своей метки (§99.9) — прятать его нельзя.
  it("узел с неизвестной группой показывается без группы", () => {
    const sections = groupNodes([node("a", "gone"), node("b", "g1")], GROUPS);
    expect(sections[0].group).toBeNull();
    expect(sections[0].nodes.map((n) => n.id)).toEqual(["a"]);
    expect(sections[1].group?.id).toBe("g1");
  });

  it("пустой справочник — всё в одну секцию без группы", () => {
    const sections = groupNodes([node("a", "g1"), node("b")], []);
    expect(sections).toHaveLength(1);
    expect(sections[0].group).toBeNull();
    expect(sections[0].nodes.map((n) => n.id)).toEqual(["a", "b"]);
  });

  it("пустой список узлов — ни одной секции", () => {
    expect(groupNodes([], GROUPS)).toEqual([]);
  });

  it("секция без группы отсутствует, если все узлы сгруппированы", () => {
    const sections = groupNodes([node("a", "g1")], GROUPS);
    expect(sections.every((s) => s.group !== null)).toBe(true);
  });
});

describe("usedGroups", () => {
  it("оставляет только группы, встречающиеся у узлов, в порядке справочника", () => {
    // Узлы намеренно идут «третья группа, первая» — порядок берётся из
    // справочника, а не из порядка появления.
    const used = usedGroups([node("c", "g3"), node("a", "g1"), node("x")], GROUPS);
    expect(used.map((g) => g.id)).toEqual(["g1", "g3"]);
  });

  it("узлов нет — ни одной группы (селект фильтра прячется целиком)", () => {
    expect(usedGroups([], GROUPS)).toEqual([]);
    expect(usedGroups([node("a"), node("b")], GROUPS)).toEqual([]);
  });

  it("ссылка на группу вне справочника не добавляет пункт", () => {
    expect(usedGroups([node("a", "gone")], GROUPS)).toEqual([]);
  });
});

describe("hasUngroupedNodes", () => {
  it("true, только когда есть узел без группы", () => {
    expect(hasUngroupedNodes([node("a", "g1"), node("b")])).toBe(true);
    expect(hasUngroupedNodes([node("a", "g1")])).toBe(false);
    expect(hasUngroupedNodes([])).toBe(false);
  });
});

describe("hasGroupedSections", () => {
  it("false, когда групп нет — экран рендерится как до §99", () => {
    expect(hasGroupedSections(groupNodes([node("a"), node("b")], GROUPS))).toBe(false);
    expect(hasGroupedSections(groupNodes([node("a", "gone")], GROUPS))).toBe(false);
  });

  it("true, когда есть хоть одна группа", () => {
    expect(hasGroupedSections(groupNodes([node("a"), node("b", "g1")], GROUPS))).toBe(true);
  });
});

describe("свёрнутые группы", () => {
  beforeEach(() => window.localStorage.clear());

  it("сохраняются и читаются", () => {
    saveCollapsed(new Set(["g1", "g2"]));
    expect([...loadCollapsed()].sort()).toEqual(["g1", "g2"]);
  });

  it("пустое множество стирает ключ, а не пишет пустой массив", () => {
    saveCollapsed(new Set(["g1"]));
    saveCollapsed(new Set());
    expect(window.localStorage.getItem("nexus.overview.groups.collapsed")).toBeNull();
    expect(loadCollapsed().size).toBe(0);
  });

  // Развёрнутый список — безопасный дефолт: свёрнутый по ошибке читался бы как
  // пропавшие узлы.
  it("мусор в хранилище даёт пустое множество", () => {
    window.localStorage.setItem("nexus.overview.groups.collapsed", "не json");
    expect(loadCollapsed().size).toBe(0);
    window.localStorage.setItem("nexus.overview.groups.collapsed", '{"g1":true}');
    expect(loadCollapsed().size).toBe(0);
    window.localStorage.setItem("nexus.overview.groups.collapsed", '["g1", 42, null]');
    expect([...loadCollapsed()]).toEqual(["g1"]);
  });
});
