import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TooltipProvider } from "@radix-ui/react-tooltip";

import { QueueTab } from "./QueueTab";
import { type Node } from "../../api/client";
import { ConfirmContext } from "../../lib/confirm";

// §72.3: список «Неудачные доставки» листается скроллом.
//
// Регресс, красный на прежнем коде: запрос шёл с жёстким limit=50 и без
// пагинации вообще (обычный useQuery), поэтому при 340 неудачах KPI показывал
// 340, а увидеть можно было ровно 50 — как в журнале логов до §72.2.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const PAGE_SIZE = 50;
const LAST_PAGE_SIZE = 10;
const TOTAL_ROWS = PAGE_SIZE * 2 + LAST_PAGE_SIZE;
// Sync-узел: секция живой очереди Kafka не рендерится (§69.1), в тесте только
// «Неудачные доставки».
const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika", root_method: "request" } as Node;

let logsCalls: Record<string, unknown>[] = [];

function mockServer() {
  logsCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/nodes/n1/logs") {
      logsCalls.push(params ?? {});
      // Третья страница неполная — признак конца истории (items.length < limit).
      const size = logsCalls.length >= 3 ? LAST_PAGE_SIZE : PAGE_SIZE;
      const page = Array.from({ length: size }, (_, i) => {
        const n = logsCalls.length * PAGE_SIZE + i;
        return {
          id: `id-${n}`,
          url: "http://vika-webservices.vz78.vozovoz.ru/api",
          http_method: "POST",
          method: "task.getFiles",
          status: 0,
          duration_ms: 50000,
          date_request: new Date(Date.UTC(2026, 6, 21, 10, 0, 0) - n * 60_000).toISOString(),
          done: false,
        };
      });
      return Promise.resolve({ items: page, logs_available: true });
    }
    if (url === "/api/nodes/n1/logs/failed-count") {
      return Promise.resolve({ count: 340, logs_configured: true, logs_available: true });
    }
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    return Promise.resolve({});
  });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <ConfirmContext.Provider value={() => Promise.resolve(true)}>
            {children}
          </ConfirmContext.Provider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<QueueTab node={NODE} />, { wrapper });
}

describe("QueueTab — неудачные доставки (§72.3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });

  // §79.1: список спрашивает НЕДОСТАВЛЕННЫЕ ЗАПИСИ (unresolved), а не строки
  // done=0. На старом параметре KPI (он уже считал записи) показывал 0, а список
  // — запись, доставленную повтором: расхождение внутри одного экрана, найденное
  // на стенде 2026-08-05.
  it("список фильтруется сервером по unresolved и догружает страницы до конца истории", async () => {
    renderTab();

    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    expect(logsCalls[0].unresolved).toBe("1");
    expect(logsCalls[0].done).toBeUndefined();
    expect(logsCalls[0].limit).toBe(PAGE_SIZE);
    // Первый запрос — без курсора (первая страница).
    expect(logsCalls[0].before_id).toBeUndefined();

    // Прежний код останавливался на 50 строках: обычный useQuery с жёстким
    // limit=50, никакой пагинации.
    await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(TOTAL_ROWS));

    // Каждая следующая страница несёт keyset-курсор и тот же серверный фильтр.
    expect(logsCalls.length).toBe(3);
    for (const call of logsCalls.slice(1)) {
      expect(call.unresolved).toBe("1");
      expect(call.before_id).toBeDefined();
      expect(call.to).toBeDefined();
    }
  });

  it("подгрузка останавливается на неполной странице", async () => {
    renderTab();
    await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(TOTAL_ROWS));
    // Ещё немного времени: если признак конца истории не работает, хук уйдёт за
    // четвёртой страницей и счётчик запросов вырастет.
    await new Promise((r) => setTimeout(r, 100));
    expect(logsCalls.length).toBe(3);
  });
});
