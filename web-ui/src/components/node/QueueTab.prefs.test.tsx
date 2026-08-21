import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §92: вкладка «Очередь» берёт стартовый период из того же префа, что и «Обзор»
// (node.period) — горизонт наблюдения общий для узла, а не для вкладки.
//
// Здесь же — раскладка KPI-строки: у sync-узла плитки «Ожидают отправки» не
// существует в принципе, поэтому «Неудачные доставки» занимают всю ширину;
// у async сосед есть, и строка остаётся сеткой. Красное на коде до правки: там
// одинокая плитка всегда сжималась до max-w-xs.

const { apiGet, apiPut, apiPost } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPut: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

vi.mock("../../lib/useCurrentRole", () => ({ useRoleAtLeast: () => true }));

import { QueueTab } from "./QueueTab";
import { ConfirmProvider } from "../ui/ConfirmProvider";
import { PREF_KEY_NODE_PERIOD } from "../../lib/prefs";
import { type Node } from "../../api/client";

const TEAM_ID = "763e7f70-80d3-4762-8e8c-585098a8402a";

function node(rootMethod: string): Node {
  return {
    id: "e557e4b9-55ce-455c-8bfc-9996d568a7c6",
    path: "acs_sigur",
    team_id: TEAM_ID,
    root_method: rootMethod,
    status: "enabled",
    clickhouse_table: "nexus_webhook.acs_sigur",
  } as unknown as Node;
}

let prefsItems: Array<{ team_id: string; key: string; value: unknown }> = [];

// failedCountCalls — обращения за счётчиком неудачных доставок: именно они несут
// границы периода, по ним и видно, с каким окном открылась вкладка.
function failedCountCalls() {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).includes("/logs/failed-count"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

function renderTab(n: Node) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/nodes/n1?tab=queue"]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <ConfirmProvider>
            <QueueTab node={n} />
          </ConfirmProvider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiPut.mockReset();
  apiPost.mockReset();
  apiPut.mockResolvedValue({});
  prefsItems = [];

  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/prefs") return Promise.resolve({ items: prefsItems });
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs/failed-count")) return Promise.resolve({ count: 0, logs_available: true });
    if (url.includes("/async-queue")) return Promise.resolve({ items: [], capped: false });
    if (url.includes("/breaker")) return Promise.resolve({ configured: false });
    return Promise.resolve({ items: [] });
  });
});

describe("QueueTab: дефолтный период из префа (§92)", () => {
  it("преф команды применяется с первого запроса", async () => {
    prefsItems = [
      { team_id: TEAM_ID, key: PREF_KEY_NODE_PERIOD, value: { kind: "preset", range: "7d" } },
    ];
    renderTab(node("requestAsync"));

    await waitFor(() => expect(failedCountCalls().length).toBeGreaterThan(0));
    const { from, to } = failedCountCalls()[0];
    // 7 суток ± минута на дорогу от расчёта окна до проверки.
    const spanMs = Date.parse(to) - Date.parse(from);
    expect(Math.abs(spanMs - 7 * 24 * 3600_000)).toBeLessThan(60_000);
  });

  it("без префа окно остаётся системным (24ч)", async () => {
    renderTab(node("requestAsync"));

    await waitFor(() => expect(failedCountCalls().length).toBeGreaterThan(0));
    const { from, to } = failedCountCalls()[0];
    const spanMs = Date.parse(to) - Date.parse(from);
    expect(Math.abs(spanMs - 24 * 3600_000)).toBeLessThan(60_000);
  });
});

describe("QueueTab: раскладка KPI-строки", () => {
  it("у sync-узла «Неудачные доставки» занимают всю ширину", async () => {
    renderTab(node("request"));

    const tile = await screen.findByText("queue.kpi.failed");
    // Обёртка строки — родитель карточки плитки.
    const row = tile.closest("div")?.parentElement?.parentElement;
    expect(row?.className).toContain("w-full");
    expect(row?.className).not.toContain("max-w-xs");
    // Плитки «Ожидают отправки» у sync-узла нет вовсе.
    expect(screen.queryByText("queue.kpi.pending")).toBeNull();
  });

  it("у async-узла строка остаётся сеткой из двух колонок", async () => {
    renderTab(node("requestAsync"));

    const tile = await screen.findByText("queue.kpi.failed");
    const row = tile.closest("div")?.parentElement?.parentElement;
    expect(row?.className).toContain("grid");
    expect(screen.getByText("queue.kpi.pending")).toBeTruthy();
  });
});
