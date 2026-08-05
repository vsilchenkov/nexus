import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §79.4/§79.5: вкладка «Метрики» шлёт те же фильтры, что журнал логов, и
// «Шаг графика». Регресс: до раздела в /api/metrics/nodes/:id уходил только
// период — отфильтровать метрики было нечем, плотность выбирал сервер.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { MetricsTab } from "./MetricsTab";
import { type Node } from "../../api/client";

const NODE = { id: "n1", path: "demo", clickhouse_table: "db.t" } as unknown as Node;

const METRICS = {
  kpi: { total: 42, delivered: 40, errors: 2, p95_ms: 87, p99_ms: 142 },
  series: [{ ts: 1, count: 2, errors: 0 }],
  chart_available: true,
  range_ms: 3600_000,
  step_seconds: 1800,
  chart_unit: "records",
};

function metricsCalls() {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).startsWith("/api/metrics/nodes/"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter>
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
  apiGet.mockImplementation((url: string) => {
    if (url.startsWith("/api/metrics/nodes/")) return Promise.resolve(METRICS);
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs/methods")) return Promise.resolve({ items: ["POST"] });
    if (url.includes("/logs/client-hosts")) return Promise.resolve({ items: [] });
    return Promise.resolve({});
  });
});

describe("MetricsTab: фильтры и шаг графика (§79.4/§79.5)", () => {
  it("без выбора шага параметр не отправляется (сервер решает сам)", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].step).toBeUndefined();
  });

  it("выбор шага уходит в запрос", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    const stepGroup = within(screen.getByRole("group", { name: "metrics.step.label" }));
    fireEvent.click(stepGroup.getByRole("button", { name: "metrics.range.24h" }));
    await waitFor(() => expect(metricsCalls().some((c) => c.step === "24h")).toBe(true));
  });

  it("быстрый фильтр «Ошибки» уходит в запрос метрик", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));
    fireEvent.click(screen.getByRole("button", { name: "logs.filter.err" }));
    await waitFor(() => expect(metricsCalls().some((c) => c.status === "err")).toBe(true));
  });

  it("полнотекстовый фильтр применяется по Enter, а не на каждую букву (§77.3)", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));

    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    const before = metricsCalls().length;
    fireEvent.change(input, { target: { value: "order" } });
    expect(metricsCalls().length).toBe(before);

    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(metricsCalls().some((c) => c.q === "order")).toBe(true));
  });

  it("панель фильтров без полей дат — окно задаёт период (§79.4)", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));

    expect(screen.queryByText("logs.advanced.from")).not.toBeInTheDocument();
    expect(screen.queryByText("logs.advanced.to")).not.toBeInTheDocument();
    expect(screen.getByText("logs.advanced.client_host")).toBeInTheDocument();
  });
});
