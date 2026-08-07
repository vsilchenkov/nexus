import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter, useLocation } from "react-router-dom";
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

// §84.2: вид вкладки живёт в адресе, поэтому тесты монтируют её на конкретном
// маршруте. LocationProbe печатает текущую строку запроса — так проверяется,
// что клик действительно ПИШЕТ адрес, а не только меняет состояние.
function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.search}</div>;
}

function locSearch(): string {
  return screen.getByTestId("loc").textContent ?? "";
}

function renderTab(entry = "/nodes/n1?tab=metrics") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <MetricsTab node={NODE} />
          <LocationProbe />
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
  // §84.3 меняет контракт: дефолт шага перестал быть «Авто». Раньше здесь
  // проверялось, что step не отправляется вовсе.
  it("дефолтный шаг конкретный: на периоде 24ч уходит step=1h, а не пусто", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].step).toBe("1h");
  });

  it("выбор «Авто» отправку шага отключает — сервер решает сам", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    const stepGroup = within(screen.getByRole("group", { name: "metrics.step.label" }));
    fireEvent.click(stepGroup.getByRole("button", { name: "metrics.step.auto" }));
    await waitFor(() => expect(metricsCalls().some((c) => c.step === undefined)).toBe(true));
  });

  it("выбор шага уходит в запрос", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    const stepGroup = within(screen.getByRole("group", { name: "metrics.step.label" }));
    fireEvent.click(stepGroup.getByRole("button", { name: "metrics.step.opt.30m" }));
    await waitFor(() => expect(metricsCalls().some((c) => c.step === "30m")).toBe(true));
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

// §84.2: вид вкладки — производная АДРЕСА. Всё ниже красное на старом коде:
// там период и шаг жили в useState и в адрес не попадали никогда.
describe("MetricsTab: период и шаг в адресе (§84.2)", () => {
  it("адрес управляет запросом: ?range=7d&step=6h доезжает до API", async () => {
    renderTab("/nodes/n1?tab=metrics&range=7d&step=6h");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("7d");
    expect(metricsCalls()[0].step).toBe("6h");
  });

  it("клик по периоду пишет адрес — ссылкой можно поделиться", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "metrics.range.7d" }));
    await waitFor(() => expect(locSearch()).toContain("range=7d"));
    await waitFor(() => expect(metricsCalls().some((c) => c.range === "7d")).toBe(true));
  });

  it("клик по шагу пишет адрес", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    const stepGroup = within(screen.getByRole("group", { name: "metrics.step.label" }));
    fireEvent.click(stepGroup.getByRole("button", { name: "metrics.step.opt.30m" }));
    await waitFor(() => expect(locSearch()).toContain("step=30m"));
  });

  it("дефолтный вид в адрес не пишется — ссылка остаётся короткой", async () => {
    renderTab("/nodes/n1?tab=metrics&range=7d");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "metrics.range.24h" }));
    await waitFor(() => expect(locSearch()).not.toContain("range="));
    expect(locSearch()).not.toContain("step=");
  });

  it("шаг, не влезающий в новый период, заменяется дефолтом нового периода", async () => {
    // 30м на окне 30 суток дало бы 1440 столбцов: сервер поднял бы шаг, а
    // сегмент показывал бы не ту ширину столбца, что получилась.
    renderTab("/nodes/n1?tab=metrics&range=24h&step=30m");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "metrics.range.30d" }));
    await waitFor(() => expect(metricsCalls().some((c) => c.range === "30d")).toBe(true));
    const last = metricsCalls()[metricsCalls().length - 1];
    expect(last.step).toBe("24h");
    expect(locSearch()).not.toContain("step=30m");
  });

  // Сужение ТОЛЬКО снизу: слишком мелкий шаг сервер молча поднял бы до потолка,
  // и кнопка врала бы о плотности. Крупные ступени остаются на любом периоде —
  // шаг больше окна это определённое поведение §79.5 (один столбец), а не
  // ошибка, и прятать их значило бы «их нет вовсе».
  it("на 30 днях нет минуты, но недельные ступени есть", async () => {
    renderTab("/nodes/n1?tab=metrics&range=30d");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    const g = within(screen.getByRole("group", { name: "metrics.step.label" }));
    expect(g.queryByRole("button", { name: "metrics.step.opt.1m" })).not.toBeInTheDocument();
    expect(g.getByRole("button", { name: "metrics.step.opt.7d" })).toBeInTheDocument();
  });

  it("на часе доступны и минута, и крупные ступени", async () => {
    renderTab("/nodes/n1?tab=metrics&range=1h");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    const g = within(screen.getByRole("group", { name: "metrics.step.label" }));
    expect(g.getByRole("button", { name: "metrics.step.opt.1m" })).toBeInTheDocument();
    expect(g.getByRole("button", { name: "metrics.step.opt.30d" })).toBeInTheDocument();
  });

  it("мусор в адресе не ломает вкладку — берётся дефолт", async () => {
    renderTab("/nodes/n1?tab=metrics&range=zzz&step=99y");
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].range).toBe("24h");
    expect(metricsCalls()[0].step).toBe("1h");
  });
});
