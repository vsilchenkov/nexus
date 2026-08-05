import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §79.3: активная вкладка узла живёт в адресе. Регресс: до раздела ?tab=
// распознавался ТОЛЬКО как "logs", а переключение вкладок в адрес не писалось —
// ссылку «вот метрики этого узла» переслать было нечем.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

// Команда узла уже активна — резолв §58 в этом тесте не участвует.
vi.mock("../lib/nodeShare", async () => {
  const actual = await vi.importActual<typeof import("../lib/nodeShare")>("../lib/nodeShare");
  return { ...actual, useEnsureNodeTeam: () => ({ status: "ready" }) };
});

vi.mock("../lib/useCurrentRole", () => ({ useRoleAtLeast: () => true }));

// Вкладки подменены маркерами: тест про маршрутизацию, а не про их содержимое.
vi.mock("../components/node/OverviewTab", () => ({
  OverviewTab: () => <div>TAB-OVERVIEW</div>,
}));
vi.mock("../components/node/LogsTab", () => ({
  LogsTab: ({ initialFilter }: { initialFilter?: { from?: string; done?: string } }) => (
    <div>
      TAB-LOGS
      <span data-testid="logs-from">{initialFilter?.from ?? "-"}</span>
      <span data-testid="logs-done">{initialFilter?.done ?? "-"}</span>
    </div>
  ),
}));
vi.mock("../components/node/ConfigTab", () => ({ ConfigTab: () => <div>TAB-CONFIG</div> }));
vi.mock("../components/node/MetricsTab", () => ({ MetricsTab: () => <div>TAB-METRICS</div> }));
vi.mock("../components/node/QueueTab", () => ({ QueueTab: () => <div>TAB-QUEUE</div> }));

import NodeDetail from "./NodeDetail";

const NODE = {
  id: "n1",
  path: "demo/exchange",
  root_method: "requestAsync",
  status: "enabled",
  clickhouse_table: "db.t",
};

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="search">{loc.search}</div>;
}

function renderAt(entry: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        <Routes>
          <Route
            path="/nodes/:id"
            element={
              <>
                <NodeDetail />
                <LocationProbe />
              </>
            }
          />
        </Routes>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1") return Promise.resolve(NODE);
    if (url === "/api/auth/me") return Promise.resolve({ user: { role: "admin" } });
    return Promise.resolve({});
  });
});

describe("NodeDetail: вкладка в адресе (§79.3)", () => {
  it("открывается сразу на вкладке из ссылки", async () => {
    renderAt("/nodes/n1?tab=metrics");
    expect(await screen.findByText("TAB-METRICS")).toBeInTheDocument();
  });

  it.each(["config", "queue", "logs"])("понимает ?tab=%s, а не только logs", async (tab) => {
    renderAt(`/nodes/n1?tab=${tab}`);
    expect(await screen.findByText(`TAB-${tab.toUpperCase()}`)).toBeInTheDocument();
  });

  it("мусорная вкладка нормализуется в обзор", async () => {
    renderAt("/nodes/n1?tab=zzz");
    expect(await screen.findByText("TAB-OVERVIEW")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("search").textContent).toContain("tab=overview"));
  });

  it("клик по вкладке пишет её в адрес", async () => {
    renderAt("/nodes/n1");
    expect(await screen.findByText("TAB-OVERVIEW")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "node.tabs.metrics" }));
    expect(await screen.findByText("TAB-METRICS")).toBeInTheDocument();
    expect(screen.getByTestId("search").textContent).toContain("tab=metrics");
  });

  it("окно журнала из адреса доезжает до вкладки логов", async () => {
    const from = Date.parse("2026-08-05T10:00:00.000Z");
    renderAt(`/nodes/n1?tab=logs&from=${from}&to=${from + 3600_000}&done=no`);
    expect(await screen.findByText("TAB-LOGS")).toBeInTheDocument();
    expect(screen.getByTestId("logs-done").textContent).toBe("no");
    expect(screen.getByTestId("logs-from").textContent).not.toBe("-");
  });

  it("уход с логов чистит окно из адреса", async () => {
    const from = Date.parse("2026-08-05T10:00:00.000Z");
    renderAt(`/nodes/n1?tab=logs&from=${from}&to=${from + 3600_000}&status=err`);
    expect(await screen.findByText("TAB-LOGS")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "node.tabs.overview" }));
    expect(await screen.findByText("TAB-OVERVIEW")).toBeInTheDocument();
    const search = screen.getByTestId("search").textContent ?? "";
    expect(search).toContain("tab=overview");
    expect(search).not.toContain("from=");
    expect(search).not.toContain("status=");
  });

  it("чужие параметры адреса переживают переключение вкладок", async () => {
    renderAt("/nodes/n1?tab=overview&team=vika");
    expect(await screen.findByText("TAB-OVERVIEW")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "node.tabs.config" }));
    expect(await screen.findByText("TAB-CONFIG")).toBeInTheDocument();
    expect(screen.getByTestId("search").textContent).toContain("team=vika");
  });
});
