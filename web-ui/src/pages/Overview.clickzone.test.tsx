import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Overview from "./Overview";
import { PREF_KEY_OVERVIEW_PERIOD } from "../lib/prefs";
import { type MyTeamsResp } from "../lib/teams";
import { TooltipProvider } from "../components/ui";
import { expectLiftedAbove, expectStretchedTo } from "../test/stretched";

// §7.3: «Клик по строке таблицы или по карточке открывает §7.4».
//
// Найдено на бою: кликабельной была только надпись — 135 px из 1033 px строки и
// 158 px из ~400 px карточки. Оператор целился в карточку, попадал в пустоту и
// считал интерфейс сломанным (правая кнопка при этом работала — она попадает
// точно по надписи, и «Открыть в новой вкладке» узел открывало).
//
// Здесь охраняется КОНТРАКТ приёма, а не пиксели: чего именно эти проверки НЕ
// доказывают — написано в src/test/stretched.ts.

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
  {
    id: "free-a",
    path: "free/a",
    team_id: TEAM,
    root_method: "request",
    status: "enabled",
    // Без таблицы логов столбцы спарклайна не кликабельны, и проверять подъём
    // было бы не над чем.
    clickhouse_table: "nexus_default.free_a",
  },
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
// Список узлов «сервера» — тесты подменяют его между прогонами.
let nodesList = NODES;
// prometheusAvailable=false воспроизводит окно, когда статусы ещё неизвестны.
let prometheusAvailable = true;

type Row = { node_id: string; in: number; out: number; errors: number; last_outcome: string };
let metrics: Row[] = [];

function ok(id: string, n: number): Row {
  return { node_id: id, in: n, out: n, errors: 0, last_outcome: "ok", spark: [1, 2, 3] } as Row;
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
    if (url === "/api/nodes") return Promise.resolve({ items: nodesList });
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
        prometheus_available: prometheusAvailable,
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
  // TooltipProvider в приложении монтируется в AppShell; столбцы спарклайна —
  // это Radix Tooltip на каждый бар, и без провайдера рендер падает.
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>{children}</TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<Overview />, { wrapper });
}

describe("Overview: зона клика по узлу (§7.3)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
    groups = [];
    nodesList = NODES;
    prometheusAvailable = true;
    metrics = NODES.map((n) => ok(n.id, 10));
    mockServer();
  });

  afterEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  async function firstNodeLink(): Promise<HTMLElement> {
    return await waitFor(() => {
      const links = screen
        .getAllByRole("link")
        .filter((a) => (a.getAttribute("href") ?? "").startsWith("/nodes/free-a"));
      expect(links).toHaveLength(1);
      return links[0];
    });
  }

  it("таблица: ссылка узла растянута по всей строке", async () => {
    localStorage.setItem("nexus.overview.view", "table");
    renderOverview();
    const link = await firstNodeLink();

    expect(link.tagName).toBe("A");
    expectStretchedTo(link, link.closest("tr") as HTMLElement);
  });

  it("карточки: ссылка узла растянута по всей карточке", async () => {
    localStorage.setItem("nexus.overview.view", "cards");
    renderOverview();
    const link = await firstNodeLink();

    expect(link.tagName).toBe("A");
    // Опора — карточка: ближайший позиционированный предок ссылки.
    const host = link.parentElement!.closest(".relative") as HTMLElement;
    expectStretchedTo(link, host);
  });

  // Столбцы спарклайна ведут в журнал за свой интервал. Под растянутой ссылкой
  // они стали бы немыми, и stopPropagation не помог бы: ссылка перехватывает
  // клик ДО цели, останавливать нечего.
  it.each(["table", "cards"])("спарклайн в раскладке %s поднят над ссылкой", async (view) => {
    localStorage.setItem("nexus.overview.view", view);
    renderOverview();
    const link = await firstNodeLink();

    const bar = await waitFor(() => {
      const el = document.querySelector(".cursor-pointer");
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expectLiftedAbove(bar, link);
  });
});
