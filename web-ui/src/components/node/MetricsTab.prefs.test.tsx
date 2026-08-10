import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §84.3: вид вкладки «Метрики» запоминается ДЛЯ КАЖДОГО УЗЛА отдельной строкой
// префа §71 (node.metrics.view.<id без дефисов>). Цепочка приоритетов:
// адрес → преф этого узла → системный дефолт.
//
// Всё в этом файле красное на старом коде: до §84.3 вкладка префов не читала
// вовсе и открывалась всегда на 24ч.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { MetricsTab } from "./MetricsTab";
import { type Node } from "../../api/client";

const NODE_ID = "e557e4b9-55ce-455c-8bfc-9996d568a7c6";
const PREF_KEY = "node.metrics.view.e557e4b955ce455c8bfc9996d568a7c6";
const TEAM_ID = "763e7f70-80d3-4762-8e8c-585098a8402a";

const NODE = {
  id: NODE_ID,
  path: "acs_sigur",
  team_id: TEAM_ID,
  clickhouse_table: "nexus_webhook.acs_sigur",
} as unknown as Node;

const METRICS = {
  kpi: { total: 4558, delivered: 4558, errors: 0, p95_ms: 30490, p99_ms: 142958 },
  series: [{ ts: 1, count: 2, errors: 0 }],
  chart_available: true,
  range_ms: 86_400_000,
  step_seconds: 3600,
  chart_unit: "records",
};

function metricsCalls() {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).startsWith("/api/metrics/nodes/"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

// prefsItems — что отдаёт /api/me/prefs. Задаётся тестом до рендера.
let prefsItems: Array<{ team_id: string; key: string; value: unknown }> = [];
let prefsFails = false;
// prefsGate — задержка ответа префов: нужна, чтобы проверить, что до их
// прихода запрос метрик не уходит вовсе.
let prefsGate: (() => void) | null = null;

function renderTab(entry = "/nodes/n1?tab=metrics") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <MetricsTab node={NODE} />
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
  prefsFails = false;
  prefsGate = null;

  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/prefs") {
      if (prefsFails) return Promise.reject(new Error("500"));
      if (prefsGate) {
        return new Promise((resolve) => {
          prefsGate = () => resolve({ items: prefsItems });
        });
      }
      return Promise.resolve({ items: prefsItems });
    }
    if (url.startsWith("/api/metrics/nodes/")) return Promise.resolve(METRICS);
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs/methods")) return Promise.resolve({ items: ["POST"] });
    if (url.includes("/logs/client-hosts")) return Promise.resolve({ items: [] });
    return Promise.resolve({});
  });
});

describe("MetricsTab: вид узла в персональных предпочтениях (§84.3)", () => {
  it("сохранённый вид узла применяется с первого запроса — 24ч не запрашивается", async () => {
    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "7d", step: "6h" } }];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("7d");
    expect(metricsCalls()[0].step).toBe("6h");
    // Именно «не было ни одного запроса с 24ч», а не «первый был 7д»: иначе
    // мигание §71 прошло бы мимо теста.
    expect(metricsCalls().some((c) => c.range === "24h")).toBe(false);
  });

  it("адрес выигрывает у префа — прямая ссылка обязана открывать что написано", async () => {
    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "7d", step: "6h" } }];
    renderTab("/nodes/n1?tab=metrics&range=1h");

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("1h");
  });

  it("до прихода префов запрос метрик не уходит вовсе (анти-мигание §71)", async () => {
    prefsGate = () => undefined; // ответ префов задержан
    renderTab();

    // Даём react-query отработать все микрозадачи: запроса метрик быть не должно.
    await waitFor(() => expect(apiGet).toHaveBeenCalledWith("/api/me/prefs"));
    expect(metricsCalls()).toHaveLength(0);

    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "7d" } }];
    prefsGate?.();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("7d");
  });

  it("ошибка /api/me/prefs не блокирует вкладку — работаем на системном дефолте", async () => {
    prefsFails = true;
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("24h");
    expect(metricsCalls()[0].step).toBe("1h");
  });

  it("преф узла без шага: период берётся из префа, шаг — дефолт этого периода", async () => {
    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "7d" } }];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("7d");
    expect(metricsCalls()[0].step).toBe("6h");
  });

  it("преф ДРУГОГО узла не применяется — ключ включает id", async () => {
    prefsItems = [
      { team_id: TEAM_ID, key: "node.metrics.view.2c7b4368d2d04795aa9a7184e2ea697c", value: { range: "7d" } },
    ];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("24h");
  });

  it("мусор в значении префа не ломает вкладку", async () => {
    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "zzz", step: 42 } }];
    renderTab();

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("24h");
  });
});

describe("MetricsTab: запись префа (§84.3)", () => {
  function prefPuts() {
    return apiPut.mock.calls
      .filter((c) => c[0] === "/api/me/prefs")
      .map((c) => c[1] as { team_id: string; key: string; value: Record<string, string> });
  }

  it("клик по периоду сохраняет вид в команду УЗЛА под ключом этого узла", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "metrics.range.7d" }));
    await waitFor(() => expect(prefPuts().length).toBeGreaterThan(0));

    const put = prefPuts()[prefPuts().length - 1];
    expect(put.key).toBe(PREF_KEY);
    expect(put.team_id).toBe(TEAM_ID);
    expect(put.value.range).toBe("7d");
  });

  it("клик по шагу сохраняет шаг", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    const stepGroup = within(screen.getByRole("group", { name: "metrics.step.label" }));
    fireEvent.click(stepGroup.getByRole("button", { name: "metrics.step.opt.30m" }));
    await waitFor(() => expect(prefPuts().length).toBeGreaterThan(0));

    expect(prefPuts()[prefPuts().length - 1].value.step).toBe("30m");
  });

  it("первая загрузка ничего не пишет — преф меняет только действие пользователя", async () => {
    prefsItems = [{ team_id: TEAM_ID, key: PREF_KEY, value: { range: "7d", step: "6h" } }];
    renderTab("/nodes/n1?tab=metrics&range=1h&step=5m");

    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    // Иначе «Назад» и любой внешний адрес переписывали бы личный дефолт узла.
    expect(prefPuts()).toHaveLength(0);
  });
});
