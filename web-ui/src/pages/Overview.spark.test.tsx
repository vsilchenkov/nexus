import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Overview from "./Overview";
import { PREF_KEY_OVERVIEW_PERIOD } from "../lib/prefs";
import { TooltipProvider } from "../components/ui";
import { type MyTeamsResp } from "../lib/teams";

// §52-доп: спарклайн карточки показывает ДАННЫЕ, а не статус узла.
//
// Прежний код красил все столбцы одним цветом по статусу: узел со статусом down
// выглядел так, будто не прошёл ни один запрос, хотя ошибок было 42 из 489.
// Тест красный на старом коде — там красными оказывались ВСЕ столбцы.

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

const NODES = [
  { id: "n1", path: "alpha/a", team_id: TEAM, root_method: "request", status: "active" },
];

// Узел лежит по статусу, но трафик шёл, и ошибки были только в одном бакете
// из четырёх — ровно ситуация со скриншота.
const SPARK = [10, 10, 10, 10];
const SPARK_ERR = [0, 0, 10, 0];

function mockServer(sparkErr: number[] | undefined) {
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
        items: [
          {
            node_id: "n1",
            node: "alpha/a",
            in: 489,
            out: 447,
            errors: 42,
            p95_ms: 1600,
            spark: SPARK,
            ...(sparkErr ? { spark_err: sparkErr } : {}),
            last_error: true,
            last_outcome: "down",
          },
        ],
        totals: { incoming: 489, outgoing: 447, errors: 42, error_rate: 8.6 },
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

function renderOverview() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={["/"]}>
      {/* В приложении провайдер монтируется один раз в AppShell (§25); в тесте
          страница рендерится отдельно, поэтому оборачиваем здесь. */}
      <TooltipProvider>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </TooltipProvider>
    </MemoryRouter>
  );
  return render(<Overview />, { wrapper });
}

describe("Overview: спарклайн показывает ошибки, а не статус (§52-доп)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
  });

  it("у узла со статусом down красным помечен только столбец с ошибками", async () => {
    mockServer(SPARK_ERR);
    const { container } = renderOverview();

    await waitFor(() => {
      expect(container.querySelectorAll("span.bg-accent").length).toBeGreaterThan(0);
    });

    // Столбцы = внешние span'ы с базовым цветом; красный сегмент — вложенный.
    const bars = container.querySelectorAll("span.bg-accent");
    expect(bars.length).toBe(SPARK.length);

    const errSegments = container.querySelectorAll("span.bg-err");
    expect(errSegments.length).toBe(1);
    // Сегмент лежит ВНУТРИ третьего столбца — того, где ошибки.
    expect(bars[2]?.contains(errSegments[0]!)).toBe(true);
  });

  it("без разбивки по ошибкам столбцы одноцветные, а не зелёные-с-нулём", async () => {
    // Старый бэкенд или Prometheus-fallback: spark_err не пришёл. Рисовать
    // «ошибок ноль» в этом случае значило бы соврать.
    mockServer(undefined);
    const { container } = renderOverview();

    await waitFor(() => {
      expect(container.querySelectorAll("span.bg-accent").length).toBe(SPARK.length);
    });
    expect(container.querySelectorAll("span.bg-err").length).toBe(0);
  });
});
