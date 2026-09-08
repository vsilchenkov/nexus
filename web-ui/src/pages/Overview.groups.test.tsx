import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Overview from "./Overview";
import { PREF_KEY_OVERVIEW_PERIOD } from "../lib/prefs";
import { type MyTeamsResp } from "../lib/teams";

// §99.5: группировка узлов по группам на экране «Узлы».
//
// Главное, что здесь проверяется: группировка НЕ пересортировывает список, а
// разбивает уже отсортированный. Поэтому деградировавший узел поднимается
// наверх СВОЕЙ группы, а не всего экрана.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const TEAM = "team-a";
const TEAMS = [
  {
    id: TEAM,
    slug: "alpha",
    name: "Alpha",
    ch_database: "nexus_alpha",
    external_url: "",
    role: "admin" as const,
  },
];

// Узлы: два без группы, три в g1, один в g2. Пути намеренно в алфавитном
// порядке — так «проблемные первыми» отличимо от «как пришло из репозитория».
const NODES = [
  { id: "free-a", path: "free/a", team_id: TEAM, root_method: "request", status: "enabled" },
  { id: "free-b", path: "free/b", team_id: TEAM, root_method: "request", status: "enabled" },
  {
    id: "erp-a",
    path: "erp/a",
    team_id: TEAM,
    root_method: "request",
    status: "enabled",
    group_id: "g1",
  },
  {
    id: "erp-b",
    path: "erp/b",
    team_id: TEAM,
    root_method: "request",
    status: "enabled",
    group_id: "g1",
  },
  {
    id: "erp-c",
    path: "erp/c",
    team_id: TEAM,
    root_method: "request",
    status: "enabled",
    group_id: "g1",
  },
  {
    id: "mp-a",
    path: "mp/a",
    team_id: TEAM,
    root_method: "request",
    status: "enabled",
    group_id: "g2",
  },
];

function grp(id: string, name: string, order: number) {
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

// Справочник приходит отсортированным сервером: g1 (10), затем g2 (20).
let groups = [grp("g1", "1С Обмен", 10), grp("g2", "Маркетплейсы", 20)];

type Row = { node_id: string; in: number; out: number; errors: number; last_outcome: string };
let metrics: Row[] = [];

function ok(id: string, n: number): Row {
  return { node_id: id, in: n, out: n, errors: 0, last_outcome: "ok" };
}
function down(id: string, n: number): Row {
  return { node_id: id, in: n, out: 0, errors: n, last_outcome: "down" };
}

function mockServer() {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      const resp: MyTeamsResp = { items: TEAMS, current_team_id: TEAM, favorites: [] };
      return Promise.resolve(resp);
    }
    if (url === "/api/me/prefs") {
      return Promise.resolve({
        items: [
          {
            team_id: TEAM,
            key: PREF_KEY_OVERVIEW_PERIOD,
            value: { kind: "preset", range: "24h" },
            updated_at: "",
          },
        ],
      });
    }
    if (url === "/api/nodes") return Promise.resolve({ items: NODES });
    if (url === "/api/node-groups") return Promise.resolve({ items: groups });
    if (url === "/api/metrics/overview") {
      return Promise.resolve({
        incoming_24h: 0,
        outgoing_24h: 0,
        kafka_queue: 0,
        errors_24h: 0,
        error_rate: 0,
        prometheus_available: true,
      });
    }
    if (url === "/api/metrics/nodes") {
      return Promise.resolve({
        items: metrics,
        totals: { incoming: 0, outgoing: 0, errors: 0, error_rate: 0 },
        prometheus_available: true,
      });
    }
    if (url === "/api/me/search-history") return Promise.resolve({ items: [] });
    if (url === "/api/settings/public") return Promise.resolve({});
    if (url === "/api/auth/me") return Promise.resolve({ user: { role: "admin" } });
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
}

function renderOverview(entry = "/") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<Overview />, { wrapper });
}

// pathOrder — узлы в порядке отрисовки (и таблица, и карточки рисуют путь
// ссылкой на /nodes/:id).
function pathOrder(): string[] {
  return screen
    .getAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "")
    .filter((h) => h.startsWith("/nodes/"))
    .map((h) => h.slice("/nodes/".length))
    .filter((id) => id !== "new");
}

async function waitForList() {
  await waitFor(() => expect(pathOrder().length).toBeGreaterThan(0));
}

// groupHeader — заголовок секции группы.
//
// Искать по тексту нельзя: имя группы есть ещё и в <option> фильтра, и
// getByText находит два элемента. Заголовок — это кнопка с aria-expanded, и
// именно она отличает секцию от пункта выпадающего списка.
function groupHeader(name: string): HTMLElement | undefined {
  return screen
    .queryAllByRole("button")
    .find((b) => b.hasAttribute("aria-expanded") && (b.textContent ?? "").includes(name));
}

// groupHeaders — все заголовки секций в порядке отрисовки.
function groupHeaders(): HTMLElement[] {
  return screen.queryAllByRole("button").filter((b) => b.hasAttribute("aria-expanded"));
}

describe("Overview: группировка узлов (§99.5)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
    groups = [grp("g1", "1С Обмен", 10), grp("g2", "Маркетплейсы", 20)];
    metrics = NODES.map((n) => ok(n.id, 10));
    mockServer();
  });

  afterEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it("узлы без группы идут первыми, затем секции в порядке справочника", async () => {
    renderOverview();
    await waitForList();
    await waitFor(() => expect(groupHeader("1С Обмен")).toBeTruthy());

    expect(pathOrder()).toEqual(["free-a", "free-b", "erp-a", "erp-b", "erp-c", "mp-a"]);
    // Ровно два заголовка: у секции без группы его нет — она внешне неотличима
    // от прежнего плоского списка.
    expect(groupHeaders()).toHaveLength(2);
    expect(groupHeaders()[0].textContent).toContain("1С Обмен");
    expect(groupHeaders()[1].textContent).toContain("Маркетплейсы");
  });

  // Главное свойство §99.5. На плоском списке erp-c уехал бы в самый верх
  // экрана, над узлами без группы.
  it("деградировавший узел поднимается наверх СВОЕЙ группы, а не всего списка", async () => {
    metrics = [
      ok("free-a", 10),
      ok("free-b", 10),
      ok("erp-a", 30),
      ok("erp-b", 20),
      down("erp-c", 5),
      ok("mp-a", 10),
    ];
    renderOverview();
    await waitForList();

    // Внутри «1С Обмен» первым стоит упавший erp-c; узлы без группы — выше него.
    // Проверка внутри waitFor: до прихода метрик все статусы «unknown», и
    // порядок совпадает с исходным — вне ожидания тест мог бы зеленеть на
    // промежуточном состоянии.
    await waitFor(() =>
      expect(pathOrder()).toEqual(["free-a", "free-b", "erp-c", "erp-a", "erp-b", "mp-a"]),
    );
  });

  it("свёрнутая группа прячет свои узлы и переживает перезагрузку", async () => {
    const first = renderOverview();
    await waitForList();
    await waitFor(() => expect(groupHeader("1С Обмен")).toBeTruthy());

    fireEvent.click(groupHeader("1С Обмен")!);
    await waitFor(() => expect(pathOrder()).toEqual(["free-a", "free-b", "mp-a"]));
    // Заголовок остаётся и помечен свёрнутым — иначе группа исчезла бы вместе
    // с узлами.
    expect(groupHeader("1С Обмен")?.getAttribute("aria-expanded")).toBe("false");

    // Перемонтирование = новая загрузка страницы: состояние в localStorage.
    first.unmount();
    renderOverview();
    await waitForList();
    await waitFor(() => expect(pathOrder()).toEqual(["free-a", "free-b", "mp-a"]));
  });

  it("фильтр по группе оставляет только её узлы", async () => {
    renderOverview("/?group=g1");
    await waitForList();
    await waitFor(() => expect(pathOrder()).toEqual(["erp-a", "erp-b", "erp-c"]));
  });

  it("фильтр «Без группы» оставляет только узлы без неё", async () => {
    renderOverview("/?group=none");
    await waitForList();
    await waitFor(() => expect(pathOrder()).toEqual(["free-a", "free-b"]));
  });

  // Групп нет вовсе — экран обязан выглядеть ровно как до §99.
  it("без групп в справочнике список плоский и без заголовков", async () => {
    groups = [];
    renderOverview();
    await waitForList();
    await waitFor(() => expect(pathOrder()).toHaveLength(6));
    expect(groupHeaders()).toHaveLength(0);
  });

  // Гонка: группу удалили в соседней вкладке. Узел важнее своей метки (§99.9).
  it("узел со ссылкой на исчезнувшую группу показывается без группы", async () => {
    groups = [grp("g2", "Маркетплейсы", 20)];
    renderOverview();
    await waitForList();
    await waitFor(() => expect(groupHeader("Маркетплейсы")).toBeTruthy());

    // erp-* потеряли свою группу и уехали к узлам без группы — но остались на
    // экране.
    expect(pathOrder()).toEqual(["free-a", "free-b", "erp-a", "erp-b", "erp-c", "mp-a"]);
    expect(groupHeaders()).toHaveLength(1);
  });
});
