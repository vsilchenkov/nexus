import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import type { CatalogNode } from "../api/client";
import {
  catalogNodesUrl,
  filterCatalogNodes,
  groupDesktopHref,
  invalidateCatalogUsage,
  isHostBindingEffective,
} from "./catalogUsage";

const node = (path: string): CatalogNode => ({
  id: path,
  path,
  target_url: "",
  url_mode: "static",
  root_method: "request",
  status: "enabled",
  team_id: "t1",
  team_slug: "a",
  team_name: "A",
});

describe("catalogUsage (§104)", () => {
  it.each([
    { kind: "group", want: "/api/node-groups/g%2F1/nodes" },
    { kind: "header", want: "/api/headers/g%2F1/nodes" },
    { kind: "host", want: "/api/allowed-hosts/g%2F1/nodes" },
  ] as const)("$kind → $want (id экранируется)", ({ kind, want }) => {
    expect(catalogNodesUrl(kind, "g/1")).toBe(want);
  });

  it("рабочий стол группы — сквозной режим и фильтр по группе", () => {
    expect(groupDesktopHref("7f1c")).toBe("/?team=*&group=7f1c");
  });

  it("фильтр по пути без учёта регистра, пустой — всё", () => {
    const items = [node("1c/Orders"), node("1c/stock")];
    expect(filterCatalogNodes(items, " ORD ").map((n) => n.path)).toEqual(["1c/Orders"]);
    expect(filterCatalogNodes(items, "")).toBe(items);
  });

  it("привязка хоста действует только при URL из запроса", () => {
    expect(isHostBindingEffective({ url_mode: "from_request" })).toBe(true);
    expect(isHostBindingEffective({ url_mode: "static" })).toBe(false);
  });

  it("правка узла сбрасывает счётчики всех справочников и открытые списки", () => {
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    invalidateCatalogUsage(qc);
    const keys = spy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey[0]);
    expect(keys.sort()).toEqual(
      ["allowed-hosts", "allowed-hosts-search", "catalog-nodes", "headers", "headers-catalog", "node-groups"].sort(),
    );
  });
});
