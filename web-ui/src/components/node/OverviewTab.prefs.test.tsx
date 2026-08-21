import { render, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §92: вкладка «Обзор» узла берёт стартовый период из префа команды
// (node.period), а не из системных 24ч. Всё в этом файле красное на коде до
// §92: там стоял useState(defaultPeriod), и преф не читался вовсе.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { OverviewTab } from "./OverviewTab";
import { PREF_KEY_NODE_PERIOD } from "../../lib/prefs";
import { type Node } from "../../api/client";

const TEAM_ID = "763e7f70-80d3-4762-8e8c-585098a8402a";

const NODE = {
  id: "e557e4b9-55ce-455c-8bfc-9996d568a7c6",
  path: "acs_sigur",
  team_id: TEAM_ID,
  clickhouse_table: "nexus_webhook.acs_sigur",
} as unknown as Node;

const METRICS = {
  kpi: { total: 10, delivered: 10, errors: 0, p95_ms: 1, p99_ms: 2 },
  series: [],
  chart_available: true,
  range_ms: 86_400_000,
  step_seconds: 3600,
  chart_unit: "records",
};

let prefsItems: Array<{ team_id: string; key: string; value: unknown }> = [];
// prefsGate — задержка ответа префов: пока он не пришёл, запрос метрик уходить
// не должен (иначе мигание и лишний поход в ClickHouse, правило §71.5).
let prefsGate: (() => void) | null = null;

function metricsCalls() {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).startsWith("/api/metrics/nodes/"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/nodes/n1"]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <OverviewTab node={NODE} onAllLogs={() => {}} />
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiPut.mockReset();
  apiPut.mockResolvedValue({});
  prefsItems = [];
  prefsGate = null;

  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/prefs") {
      if (prefsGate) {
        return new Promise((resolve) => {
          prefsGate = () => resolve({ items: prefsItems });
        });
      }
      return Promise.resolve({ items: prefsItems });
    }
    if (url.startsWith("/api/metrics/nodes/")) return Promise.resolve(METRICS);
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs")) return Promise.resolve({ items: [] });
    return Promise.resolve({});
  });
});

describe("OverviewTab: дефолтный период из префа (§92)", () => {
  it("преф команды применяется с первого запроса — 24ч не запрашивается", async () => {
    prefsItems = [
      { team_id: TEAM_ID, key: PREF_KEY_NODE_PERIOD, value: { kind: "preset", range: "7d" } },
    ];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("7d");
    // Именно «ни одного запроса с 24ч»: иначе мигание прошло бы мимо теста.
    expect(metricsCalls().some((c) => c.range === "24h")).toBe(false);
  });

  it("глобальный преф работает для команды без своего значения", async () => {
    prefsItems = [{ team_id: "", key: PREF_KEY_NODE_PERIOD, value: { kind: "preset", range: "3h" } }];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("3h");
  });

  it("преф ЧУЖОГО ключа не влияет — overview.period вкладке узла не указ", async () => {
    prefsItems = [
      { team_id: TEAM_ID, key: "overview.period", value: { kind: "preset", range: "30d" } },
    ];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("24h");
  });

  it("пока префы едут, метрики не запрашиваются вовсе", async () => {
    prefsGate = () => {};
    renderTab();

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(metricsCalls()).toHaveLength(0);
  });
});
