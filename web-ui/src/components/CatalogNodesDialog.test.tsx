import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { CatalogNode, CatalogNodesResp } from "../api/client";
import type { CatalogKind } from "../lib/catalogUsage";
import { CatalogNodesDialog } from "./CatalogNodesDialog";

// §104.5: диалог «узлы элемента справочника».

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, post: vi.fn(), del: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) =>
      o && "count" in o ? `${k}:${String(o.count)}` : k,
  }),
}));

function node(id: string, over: Partial<CatalogNode> = {}): CatalogNode {
  return {
    id,
    path: `svc/${id}`,
    target_url: `https://${id}.example/api`,
    url_mode: "static",
    root_method: "request",
    status: "enabled",
    team_id: "t1",
    team_slug: "logistics",
    team_name: "Логистика",
    ...over,
  };
}

function resp(items: CatalogNode[], over: Partial<CatalogNodesResp> = {}): CatalogNodesResp {
  return { items, total: items.length, visible: items.length, hidden: 0, ...over };
}

function renderDialog(kind: CatalogKind, usageCount: number) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(qc, "invalidateQueries");
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  render(<CatalogNodesDialog kind={kind} id="x1" name="1С Обмен" usageCount={usageCount} onClose={() => {}} />, {
    wrapper,
  });
  return { invalidate };
}

const invalidatedKeys = (spy: { mock: { calls: unknown[][] } }) =>
  spy.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey[0]);

describe("CatalogNodesDialog (§104.5)", () => {
  // Фигурные скобки обязательны: функция, возвращённая из beforeEach, для
  // vitest — teardown. mockReset() возвращает сам мок, и после теста vitest
  // вызвал бы apiGet() — в тесте 404 это отклонённый промис и ложное падение.
  beforeEach(() => {
    apiGet.mockReset();
  });

  it("строки — настоящие ссылки на узел, запрос по эндпоинту вида", async () => {
    apiGet.mockResolvedValue(resp([node("a"), node("b")]));
    renderDialog("header", 2);

    const link = await screen.findByRole("link", { name: "svc/a" });
    expect(link).toHaveAttribute("href", "/nodes/a");
    expect(screen.getByRole("link", { name: "svc/b" })).toHaveAttribute("href", "/nodes/b");
    expect(apiGet).toHaveBeenCalledWith("/api/headers/x1/nodes");
  });

  it("«не действует» — только у хоста на узле без URL из запроса", async () => {
    apiGet.mockResolvedValue(
      resp([node("static"), node("dynamic", { url_mode: "from_request", target_url: "" })]),
    );
    renderDialog("host", 2);

    const staticRow = (await screen.findByRole("link", { name: "svc/static" })).closest("li")!;
    const dynamicRow = screen.getByRole("link", { name: "svc/dynamic" }).closest("li")!;
    expect(within(staticRow).getByText("catalog_nodes.not_effective")).toBeInTheDocument();
    expect(within(dynamicRow).queryByText("catalog_nodes.not_effective")).toBeNull();
    expect(within(dynamicRow).getByText("catalog_nodes.url_from_request")).toBeInTheDocument();
  });

  it("у группы статический узел не помечается", async () => {
    apiGet.mockResolvedValue(resp([node("a")]));
    renderDialog("group", 1);
    await screen.findByRole("link", { name: "svc/a" });
    expect(screen.queryByText("catalog_nodes.not_effective")).toBeNull();
  });

  it("чужие команды — числом под списком", async () => {
    apiGet.mockResolvedValue(resp([node("a")], { total: 3, visible: 1, hidden: 2 }));
    renderDialog("group", 3);
    expect(await screen.findByText("catalog_nodes.hidden:2")).toBeInTheDocument();
  });

  it("все узлы чужие — без списка и без ссылки на рабочий стол", async () => {
    apiGet.mockResolvedValue(resp([], { total: 4, visible: 0, hidden: 4 }));
    renderDialog("group", 4);
    expect(await screen.findByText("catalog_nodes.all_hidden:4")).toBeInTheDocument();
    expect(screen.queryByRole("listitem")).toBeNull();
    expect(screen.queryByRole("link", { name: "catalog_nodes.to_desktop" })).toBeNull();
  });

  it("усечение: «показаны первые N из M»", async () => {
    apiGet.mockResolvedValue(resp([node("a")], { total: 600, visible: 600 }));
    renderDialog("group", 600);
    expect(await screen.findByText("catalog_nodes.truncated")).toBeInTheDocument();
  });

  it("ссылка на рабочий стол — только у группы, в сквозном режиме с фильтром", async () => {
    apiGet.mockResolvedValue(resp([node("a")]));
    renderDialog("group", 1);
    const desk = await screen.findByRole("link", { name: "catalog_nodes.to_desktop" });
    expect(desk).toHaveAttribute("href", "/?team=*&group=x1");
  });

  it.each<CatalogKind>(["header", "host"])("у %s ссылки на рабочий стол нет", async (kind) => {
    apiGet.mockResolvedValue(resp([node("a")]));
    renderDialog(kind, 1);
    await screen.findByRole("link", { name: "svc/a" });
    expect(screen.queryByRole("link", { name: "catalog_nodes.to_desktop" })).toBeNull();
  });

  it("фильтр по пути — только при числе строк больше восьми", async () => {
    const many = Array.from({ length: 9 }, (_, i) => node(`n${i}`));
    apiGet.mockResolvedValue(resp([...many, node("orders")]));
    renderDialog("group", 10);

    const input = await screen.findByPlaceholderText("catalog_nodes.filter");
    fireEvent.change(input, { target: { value: "ORD" } });
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "svc/orders" })).toBeInTheDocument();
  });

  it("при восьми строках фильтра нет", async () => {
    apiGet.mockResolvedValue(resp(Array.from({ length: 8 }, (_, i) => node(`n${i}`))));
    renderDialog("group", 8);
    await screen.findByRole("link", { name: "svc/n0" });
    expect(screen.queryByPlaceholderText("catalog_nodes.filter")).toBeNull();
  });

  it("total разошёлся со счётчиком строки — таблица справочника перечитывается", async () => {
    apiGet.mockResolvedValue(resp([node("a"), node("b")]));
    const { invalidate } = renderDialog("host", 5);
    await waitFor(() => expect(invalidatedKeys(invalidate)).toContain("allowed-hosts"));
  });

  it("счётчик совпал — лишней инвалидации нет", async () => {
    apiGet.mockResolvedValue(resp([node("a")]));
    const { invalidate } = renderDialog("group", 1);
    await screen.findByRole("link", { name: "svc/a" });
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("404 — «элемент удалён» и таблица перечитывается", async () => {
    apiGet.mockImplementation(() => Promise.reject({ response: { status: 404 } }));
    const { invalidate } = renderDialog("header", 1);
    expect(await screen.findByText("catalog_nodes.deleted")).toBeInTheDocument();
    await waitFor(() => expect(invalidatedKeys(invalidate)).toEqual(["headers-catalog", "headers"]));
  });
});
