import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §102: карточка «Запросы по методам» на вкладке «Метрики».
//
// Здесь проверяется то, что не видно ни компоненту рейтинга (он презентационный),
// ни хуку: что вкладка просит разбивку, что ссылка строки несёт окно периода и
// применённые фильтры, и что каждое «нечего показывать» названо своей причиной.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${Object.values(o).join(",")}` : k),
    i18n: { language: "ru" },
  }),
}));

import { MetricsTab } from "./MetricsTab";
import { type Node, type NodeMetricsResp } from "../../api/client";

const NODE = { id: "n1", path: "demo", clickhouse_table: "db.t" } as unknown as Node;

const LONG = "suggestions/api/4_1/rs/suggest/address";

const BASE: NodeMetricsResp = {
  kpi: { total: 100, delivered: 90, errors: 10, p95_ms: 87, p99_ms: 142 },
  series: [{ ts: 1, count: 2, errors: 0 }],
  chart_available: true,
  range_ms: 3600_000,
  step_seconds: 1800,
  chart_unit: "records",
  methods: [
    { method: LONG, total: 60, delivered: 55 },
    { method: "api/4_1/rs/geolocate/address", total: 30, delivered: 30 },
  ],
  methods_available: true,
  methods_truncated: false,
};

let metrics: NodeMetricsResp = BASE;

function metricsCalls(): Record<string, string>[] {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).startsWith("/api/metrics/nodes/"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

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

// Ссылки рейтинга: у строк методов есть href с ?method=…
function methodLinks(): HTMLAnchorElement[] {
  return Array.from(document.querySelectorAll("a[href*='method=']"));
}

beforeEach(() => {
  apiGet.mockReset();
  metrics = BASE;
  apiGet.mockImplementation((url: string) => {
    if (url.startsWith("/api/metrics/nodes/")) return Promise.resolve(metrics);
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs/methods")) return Promise.resolve({ items: [LONG] });
    if (url.includes("/logs/clients")) {
      return Promise.resolve({ items: [{ ip: "192.168.2.5", host: "", count: 61 }], logs_available: true });
    }
    return Promise.resolve({});
  });
});

describe("MetricsTab: карточка «Запросы по методам» (§102)", () => {
  it("вкладка просит разбивку тем же запросом, что KPI и график", async () => {
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(metricsCalls()[0].with_methods).toBe("1");
  });

  it("рисует строки рейтинга ссылками на журнал по методу за окно периода", async () => {
    renderTab("/nodes/n1?tab=metrics&range=1h");
    await waitFor(() => expect(methodLinks()).toHaveLength(2));
    const href = methodLinks()[0].getAttribute("href") ?? "";
    const q = new URLSearchParams(href.slice(href.indexOf("?")));
    expect(q.get("tab")).toBe("logs");
    expect(q.get("method")).toBe(LONG);
    // Окно — час периода, а не «всё время».
    const from = Number(q.get("from"));
    const to = Number(q.get("to"));
    expect(to - from).toBe(3600_000);
  });

  it("ссылка несёт фильтры вкладки: журнал покажет ровно то, что посчитал тултип", async () => {
    renderTab();
    await waitFor(() => expect(methodLinks()).toHaveLength(2));

    // Панель фильтров + полнотекстовый запрос (применяется по Enter, §77.3).
    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));
    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    fireEvent.change(input, { target: { value: "inn" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      const href = methodLinks()[0].getAttribute("href") ?? "";
      expect(new URLSearchParams(href.slice(href.indexOf("?"))).get("q")).toBe("inn");
    });
  });

  // §105.4: ссылка на журнал переносит и клиента — рейтинг посчитан под ним.
  it("ссылка несёт клиента (ip)", async () => {
    renderTab();
    await waitFor(() => expect(methodLinks()).toHaveLength(2));

    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));
    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.client_any" }));
    fireEvent.click(await screen.findByText("192.168.2.5"));

    await waitFor(() => {
      const href = methodLinks()[0].getAttribute("href") ?? "";
      expect(new URLSearchParams(href.slice(href.indexOf("?"))).get("ip")).toBe("192.168.2.5");
    });
  });

  it("при активном фильтре по методу карточки нет: рейтинг из одной строки не рейтинг", async () => {
    metrics = { ...BASE, methods: [{ method: LONG, total: 60, delivered: 55 }] };
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.toggle_on" }));
    // Метод выбирается комбобоксом: открываем его и берём единственный пункт.
    fireEvent.click(screen.getByRole("button", { name: "logs.advanced.method_any" }));
    const option = await screen.findByRole("option", { name: LONG });
    fireEvent.click(option);

    await waitFor(() => expect(screen.queryByText("metrics.methods.title")).toBeNull());
    expect(methodLinks()).toHaveLength(0);
  });

  it("на деградации графика до попыток карточка называет расхождение единиц", async () => {
    // §102.3: рейтинг остаётся по записям, а столбцы считают прогоны. Молчать
    // об этом нельзя: «сумма полос ≠ сумме столбцов» читается как ошибка счёта.
    metrics = { ...BASE, chart_unit: "attempts" };
    renderTab();
    await waitFor(() => expect(screen.getByText("metrics.methods.unit_mismatch")).toBeTruthy());
  });

  it("в режиме записей о расхождении единиц не сообщается", async () => {
    renderTab();
    await waitFor(() => expect(methodLinks()).toHaveLength(2));
    expect(screen.queryByText("metrics.methods.unit_mismatch")).toBeNull();
  });

  it("недоступная разбивка гасит только карточку — KPI и график живы", async () => {
    metrics = { ...BASE, methods: [], methods_available: false };
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByText("100")).toBeTruthy()); // KPI: 100 запросов
    expect(screen.queryByText("metrics.methods.title")).toBeNull();
  });

  it("узел без подпутей карточку не получает вовсе", async () => {
    // Обычный (не passthrough) узел: метод пуст у всех записей. Карточка с
    // объяснением висела бы на каждой такой странице, сообщая только о себе.
    metrics = { ...BASE, methods: [{ method: "", total: 100, delivered: 90 }] };
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByText("100")).toBeTruthy());
    expect(screen.queryByText("metrics.methods.title")).toBeNull();
  });

  it("единственный подпуть — карточки нет: сравнивать не с чем", async () => {
    metrics = { ...BASE, methods: [{ method: LONG, total: 100, delivered: 90 }] };
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getByText("100")).toBeTruthy());
    expect(screen.queryByText("metrics.methods.title")).toBeNull();
  });

  it("два подпути — карточка уже есть: это и есть минимальный рейтинг", async () => {
    // Граница: один метод — нечего сравнивать, два — уже разбивка.
    renderTab();
    await waitFor(() => expect(screen.getByText("metrics.methods.title")).toBeTruthy());
    expect(methodLinks()).toHaveLength(2);
  });

  it("без таблицы логов карточки нет вовсе — сверху уже висит общая подсказка", async () => {
    metrics = { ...BASE, chart_available: false, methods: [], methods_available: false };
    renderTab();
    await waitFor(() => expect(metricsCalls().length).toBeGreaterThan(0));
    expect(screen.queryByText("metrics.methods.title")).toBeNull();
  });
});
