import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogsTab } from "./LogsTab";
import { type Node } from "../../api/client";

// §102.6: ссылка из рейтинга методов открывает журнал ПОД теми же фильтрами,
// под которыми посчитан тултип. Раньше переносились только from/to/status/done —
// строка обещала «41 запрос», а журнал показывал все записи окна.
//
// Проверяем ПЕРВЫЙ запрос: фильтр, применённый вторым, означал бы мигание
// неотфильтрованного списка и лишний поход в ClickHouse.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  // i18n.language читает панель расширенных фильтров (форматы дат) — без него
  // открытая панель падает, а именно её раскрытие тест и проверяет.
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o?.n === undefined ? k : `${k}:${o.n}`),
    i18n: { language: "ru" },
  }),
}));

const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika" } as Node;

let logsCalls: Record<string, unknown>[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  logsCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/nodes/n1/logs") {
      logsCalls.push(params ?? {});
      return Promise.resolve({ items: [], logs_available: true });
    }
    if (url === "/api/nodes/n1/logs/count") return Promise.resolve({ total: 0, logs_available: true });
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    return Promise.resolve({});
  });
});

function renderTab(initialFilter?: Parameters<typeof LogsTab>[0]["initialFilter"]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <TooltipProvider>{children}</TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<LogsTab node={NODE} initialFilter={initialFilter} />, { wrapper });
}

describe("LogsTab: фильтры дип-линка (§102.6)", () => {
  it("метод из ссылки уходит на сервер ПЕРВЫМ запросом", async () => {
    renderTab({ method: "suggestions/api/4_1/rs/suggest/address" });
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    expect(logsCalls[0].method).toBe("suggestions/api/4_1/rs/suggest/address");
  });

  it("полнотекстовый запрос с режимами и хост клиента переносятся целиком", async () => {
    renderTab({ q: "inn", qCase: true, qWord: false, qRegex: true, clientHost: "app-01" });
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    const first = logsCalls[0];
    expect(first.q).toBe("inn");
    expect(first.q_case).toBe("1");
    expect(first.q_regex).toBe("1");
    expect(first.q_word).toBeUndefined();
    expect(first.client_host).toBe("app-01");
  });

  it("панель расширенных фильтров раскрыта — видно, чем отобран список", async () => {
    renderTab({ method: "v1/suggest" });
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    // Кнопка панели переключилась в состояние «свернуть» — значит панель открыта.
    expect(screen.getByText("logs.advanced.toggle_off")).toBeTruthy();
  });

  it("без фильтров панель закрыта, а параметры не уходят", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    expect(screen.getByText("logs.advanced.toggle_on")).toBeTruthy();
    expect(logsCalls[0].method).toBeUndefined();
    expect(logsCalls[0].q).toBeUndefined();
    expect(logsCalls[0].client_host).toBeUndefined();
  });
});
