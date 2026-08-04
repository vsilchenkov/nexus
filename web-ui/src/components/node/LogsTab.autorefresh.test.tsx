import { TooltipProvider } from "@radix-ui/react-tooltip";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { LogsTab } from "./LogsTab";
import { type Node } from "../../api/client";
import { type LogRow } from "./types";

// §77.1/§77.3: инкрементальное обновление и автопоиск.
//
// Регресс, красный на прежнем коде:
//   - раскрытое тело закрывалось через 5 с, потому что авто-рефетч
//     useInfiniteQuery перезапрашивал ВСЕ страницы и пересоздавал список;
//   - поиск применялся только кнопкой «Применить».

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  // i18n нужен полям дат (LogDateField выбирает локаль календаря).
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

const PAGE_SIZE = 50;
const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika" } as Node;
const BASE_TS = Date.UTC(2026, 7, 1, 12, 0, 0);

let logsCalls: Record<string, unknown>[] = [];
let tailCalls: Record<string, unknown>[] = [];
// tailRows — что отдаёт tail-poll (запрос списка с параметром from и без курсора).
let tailRows: LogRow[] = [];

function row(i: number, tsShiftMs = 0): LogRow {
  return {
    id: `id-${i}`,
    url: "http://example/api",
    http_method: "POST",
    method: `m.${i}`,
    status: 200,
    duration_ms: 10,
    date_request: new Date(BASE_TS + tsShiftMs - i * 60_000).toISOString(),
    done: true,
  };
}

function mockServer() {
  logsCalls = [];
  tailCalls = [];
  tailRows = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    const p = params ?? {};
    if (url === "/api/nodes/n1/logs") {
      // tail-poll узнаётся по from без keyset-курсора (§77.1).
      if (p.from && !p.before_id) {
        tailCalls.push(p);
        return Promise.resolve({ items: tailRows, logs_available: true });
      }
      const pageIdx = logsCalls.length; // 0 для первой страницы
      logsCalls.push(p);
      return Promise.resolve({
        items: Array.from({ length: PAGE_SIZE }, (_, i) => row(pageIdx * PAGE_SIZE + i)),
        logs_available: true,
      });
    }
    if (url === "/api/nodes/n1/logs/count") {
      return Promise.resolve({ total: 1000, logs_available: true });
    }
    if (url.startsWith("/api/nodes/n1/log/")) {
      return Promise.resolve({
        id: "id-0",
        request: "REQUEST-BODY",
        response: "RESPONSE-BODY",
        request_runes: 12,
        response_runes: 13,
      });
    }
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    return Promise.resolve({});
  });
}

function renderTab(initialFilter?: Parameters<typeof LogsTab>[0]["initialFilter"]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        {/* Панель расширенных фильтров содержит LabelHint (Radix Tooltip). */}
        <TooltipProvider>{children}</TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const r = render(<LogsTab node={NODE} initialFilter={initialFilter} />, { wrapper });
  // jsdom не считает layout: без размеров контейнера срабатывает автодогрузка
  // «страница короче контейнера» и список тянет страницы до потолка.
  const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
  if (wrap) {
    Object.defineProperty(wrap, "scrollHeight", { value: 5000, configurable: true });
    Object.defineProperty(wrap, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(wrap, "scrollTop", { value: 0, writable: true, configurable: true });
  }
  return r;
}

const lastLogsCall = () => logsCalls[logsCalls.length - 1];

describe("LogsTab — инкрементальное обновление (§77.1)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("обновление тянет только новые записи и не перезапрашивает страницу", async () => {
    renderTab();
    await waitFor(() => expect(screen.getAllByText(/^m\./).length).toBe(PAGE_SIZE));
    const pagesAfterFirstLoad = logsCalls.length;

    // Тик tail-poll: новая запись сверху.
    tailRows = [row(999, 10 * 60_000)];
    await waitFor(() => expect(tailCalls.length).toBeGreaterThan(0), { timeout: 7000 });

    // Запрос списка (страница) НЕ повторялся — обновление инкрементальное.
    expect(logsCalls.length).toBe(pagesAfterFirstLoad);
    // Нижняя граница взята с запасом (секундная точность date_request).
    const since = new Date(tailCalls[0].from as string).getTime();
    expect(since).toBeLessThan(new Date(BASE_TS).getTime());
  }, 15000);

  it("раскрытое тело переживает тик обновления", async () => {
    renderTab();
    await waitFor(() => expect(screen.getAllByText(/^m\./).length).toBe(PAGE_SIZE));

    fireEvent.click(screen.getByText("m.1"));
    await waitFor(() => expect(screen.getByText("REQUEST-BODY")).toBeTruthy());

    // Пришли новые записи — строка с раскрытым телом обязана остаться раскрытой.
    // Ждать приходится дольше одного тика: первый tail-запрос уходит сразу при
    // включении поллинга, ещё с пустым ответом.
    tailRows = [row(998, 10 * 60_000)];
    await waitFor(() => expect(screen.getByText("m.998")).toBeTruthy(), { timeout: 12000 });
    expect(screen.getByText("REQUEST-BODY")).toBeTruthy();
  }, 20000);

  it("возврат к верху не схлопывает страницы, пока строка раскрыта", async () => {
    renderTab();
    const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
    expect(wrap).not.toBeNull();

    await waitFor(() => expect(screen.getAllByText(/^m\./).length).toBe(PAGE_SIZE));
    wrap!.scrollTop = 4400;
    fireEvent.scroll(wrap!);
    await waitFor(() => expect(screen.getAllByText(/^m\./).length).toBe(PAGE_SIZE * 2));

    // Раскрываем строку со второй страницы и возвращаемся наверх.
    fireEvent.click(screen.getByText("m.50"));
    await waitFor(() => expect(screen.getByText("REQUEST-BODY")).toBeTruthy());
    wrap!.scrollTop = 0;
    fireEvent.scroll(wrap!);

    // Страницы на месте (иначе раскрытая строка исчезла бы вместе с ними).
    await waitFor(() => expect(screen.getAllByText(/^m\./).length).toBe(PAGE_SIZE * 2));
    expect(screen.getByText("REQUEST-BODY")).toBeTruthy();
  }, 15000);
});

describe("LogsTab — автопоиск без кнопки «Применить» (§77.3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });

  it("кнопки «Применить» нет", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByText("logs.advanced.toggle_on"));
    expect(screen.queryByText("logs.advanced.apply")).toBeNull();
  });

  it("набор текста не ищет; Enter — ищет; повторный blur без изменений — нет", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByText("logs.advanced.toggle_on"));

    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    const before = logsCalls.length;

    fireEvent.change(input, { target: { value: "e" } });
    fireEvent.change(input, { target: { value: "er" } });
    fireEvent.change(input, { target: { value: "err" } });
    // Ни одного запроса на буквы — полнотекст по всей истории стоит секунды.
    expect(logsCalls.length).toBe(before);

    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(lastLogsCall().q).toBe("err"));
    const afterEnter = logsCalls.length;

    // Enter уже применил это значение — blur не должен слать второй такой же запрос.
    fireEvent.blur(input);
    await new Promise((r) => setTimeout(r, 50));
    expect(logsCalls.length).toBe(afterEnter);
  }, 15000);

  it("уход фокуса применяет введённое значение", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByText("logs.advanced.toggle_on"));

    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    fireEvent.change(input, { target: { value: "timeout" } });
    fireEvent.blur(input);

    await waitFor(() => expect(lastLogsCall().q).toBe("timeout"));
  });

  it("Escape возвращает применённое значение и не ищет", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByText("logs.advanced.toggle_on"));

    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder") as HTMLInputElement;
    const before = logsCalls.length;
    fireEvent.change(input, { target: { value: "черновик" } });
    fireEvent.keyDown(input, { key: "Escape" });

    await waitFor(() => expect(input.value).toBe(""));
    expect(logsCalls.length).toBe(before);
  });

  it("«Сбросить» очищает фильтр одним запросом, без лишнего поиска от blur", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    fireEvent.click(screen.getByText("logs.advanced.toggle_on"));

    const input = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    fireEvent.change(input, { target: { value: "err" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(lastLogsCall().q).toBe("err"));
    const afterSearch = logsCalls.length;

    // mousedown гасится обработчиком — blur не успевает применить черновик.
    const reset = screen.getByText("logs.advanced.reset");
    fireEvent.mouseDown(reset);
    fireEvent.click(reset);

    await waitFor(() => expect(lastLogsCall().q).toBeUndefined());
    expect(logsCalls.length).toBe(afterSearch + 1);
  }, 15000);
});
