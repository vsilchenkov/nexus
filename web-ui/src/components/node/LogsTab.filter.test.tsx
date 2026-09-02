import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogsTab } from "./LogsTab";
import { type Node } from "../../api/client";
import { type LogRow } from "./types";

// §72.2: быстрые фильтры «Ошибки» и «В работе» уходят НА СЕРВЕР.
//
// Регресс, красный на прежнем коде: список запрашивался без status/done и
// фильтровался в браузере по уже загруженной странице, а счётчик «из M» считался
// сервером по всей таблице. На боевом узле это давало «Показано 3 из 33», и
// остальные 30 ошибок нельзя было ни увидеть, ни доскроллить.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

// i18n в тестах не инициализирован — t() возвращает ключ; проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const PAGE_SIZE = 50;
const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika" } as Node;

// logsCalls — параметры каждого запроса списка: по ним видно и что ушло на
// сервер, и сколько страниц запрошено.
let logsCalls: Record<string, unknown>[] = [];
let countCalls: Record<string, unknown>[] = [];

// row — запись лога: ошибка = таймаут (status=0, done=false), как на бою.
function row(i: number, ok: boolean): LogRow {
  return {
    id: `id-${ok ? "ok" : "err"}-${i}`,
    url: "http://vika-webservices.vz78.vozovoz.ru/api",
    http_method: "POST",
    method: "task.getFiles",
    status: ok ? 200 : 0,
    duration_ms: ok ? 12 : 50000,
    // Даты убывают — курсор следующей страницы берётся из последней строки.
    date_request: new Date(Date.UTC(2026, 6, 21, 10, 0, 0) - i * 60_000).toISOString(),
    done: ok,
  };
}

// mockServer — «таблица» из 33 ошибок среди прочих записей (боевая пропорция).
// Без фильтра страница почти вся успешная, с фильтром — сплошь ошибки: если
// фронт не пошлёт status, в таблице окажется горстка строк.
function mockServer() {
  logsCalls = [];
  countCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/nodes/n1/logs") {
      const p = params ?? {};
      logsCalls.push(p);
      const onlyErrors = p.status === "err" || p.done === "no";
      const page = Array.from({ length: PAGE_SIZE }, (_, i) =>
        row(logsCalls.length * PAGE_SIZE + i, onlyErrors ? false : i >= 3),
      );
      return Promise.resolve({ items: page, logs_available: true });
    }
    if (url === "/api/nodes/n1/logs/count") {
      countCalls.push(params ?? {});
      return Promise.resolve({ total: 33, logs_available: true });
    }
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    return Promise.resolve({});
  });
}

function renderTab(initialFilter?: Parameters<typeof LogsTab>[0]["initialFilter"]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<LogsTab node={NODE} initialFilter={initialFilter} />, { wrapper });
}

const lastLogsCall = () => logsCalls[logsCalls.length - 1];

describe("LogsTab — серверная фильтрация (§72.2)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });

  it("клик по «Ошибки» отправляет status=err и показывает целую страницу ошибок", async () => {
    renderTab();
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    expect(lastLogsCall().status).toBeUndefined();

    fireEvent.click(screen.getByText("logs.filter.err"));

    await waitFor(() => expect(lastLogsCall().status).toBe("err"));
    // Смена фильтра обязана перезапросить список (status в queryKey), а не
    // просто спрятать строки уже загруженной страницы.
    await waitFor(() =>
      expect(screen.getAllByText("task.getFiles").length).toBeGreaterThanOrEqual(PAGE_SIZE),
    );
  });

  // §77.4: своего переключателя у done больше нет (доказанный дубль «OK/Ошибок»),
  // но параметр живёт: дип-линк из «Очереди» обязан фильтровать ПЕРВЫМ запросом
  // и быть видимым — иначе список молча показывает подмножество.
  it("дип-линк done=no фильтрует на сервере и показывается снимаемым чипом", async () => {
    renderTab({ done: "no" });
    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    expect(logsCalls[0].done).toBe("no");

    // Сегмента «Все|Завершено|В работе» в шапке нет.
    expect(screen.queryByText("logs.filter.done_all")).toBeNull();

    // Чип виден и снимается — после снятия параметр не уходит.
    fireEvent.click(screen.getByText("logs.filter.done_no"));
    await waitFor(() => expect(lastLogsCall().done).toBeUndefined());
  });

  it("список и счётчик спрашивают ОДИН набор фильтров", async () => {
    renderTab();
    await waitFor(() => expect(countCalls.length).toBeGreaterThan(0));

    fireEvent.click(screen.getByText("logs.filter.err"));

    await waitFor(() => expect(lastLogsCall().status).toBe("err"));
    await waitFor(() => {
      const c = countCalls[countCalls.length - 1];
      expect(c.status).toBe("err");
      // count не постраничный — limit ему не отправляется.
      expect(c.limit).toBeUndefined();
    });
  });

  it("возврат к верху схлопывает накопленные страницы", async () => {
    renderTab();

    const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
    expect(wrap).not.toBeNull();
    Object.defineProperty(wrap!, "scrollHeight", { value: 5000, configurable: true });
    Object.defineProperty(wrap!, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(wrap!, "scrollTop", { value: 0, writable: true, configurable: true });

    await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(PAGE_SIZE));

    // Листаем историю: две дополнительные страницы.
    for (const want of [2, 3]) {
      wrap!.scrollTop = 4400;
      fireEvent.scroll(wrap!);
      await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(PAGE_SIZE * want));
    }

    // Возврат наверх: авто-рефетч (каждые 5 с) иначе перезапрашивал бы ВСЕ три
    // страницы разом — а наверху нужны только свежие записи.
    wrap!.scrollTop = 0;
    fireEvent.scroll(wrap!);
    await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(PAGE_SIZE));
  });

  it("подгрузка следующей страницы сохраняет фильтр и несёт keyset-курсор", async () => {
    renderTab({ status: "err" });

    // jsdom не считает layout, поэтому размеры контейнера задаём сами — и
    // делаем это ДО прихода данных: иначе при нулевой высоте сработает
    // автодогрузка «страница короче контейнера» и подменит проверяемый сценарий.
    const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
    expect(wrap).not.toBeNull();
    Object.defineProperty(wrap!, "scrollHeight", { value: 5000, configurable: true });
    Object.defineProperty(wrap!, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(wrap!, "scrollTop", { value: 0, writable: true, configurable: true });

    await waitFor(() => expect(logsCalls.length).toBeGreaterThan(0));
    // Дип-линк фильтруется ПЕРВЫМ же запросом, а не после загрузки страницы.
    expect(logsCalls[0].status).toBe("err");
    // Ждём отрисовки страницы: до неё hasNextPage ещё false, и обработчик
    // скролла ничего бы не подгрузил.
    await waitFor(() => expect(screen.getAllByText("task.getFiles")).toHaveLength(PAGE_SIZE));

    wrap!.scrollTop = 4400; // < SCROLL_BOTTOM_THRESHOLD_PX до низа
    fireEvent.scroll(wrap!);

    // Ждём именно ЗАПРОС СЛЕДУЮЩЕЙ СТРАНИЦЫ, а не «второй запрос вообще»:
    // рядом живёт авто-рефетч (каждые 5 с), который ходит без курсора, и на
    // медленной машине он успевает встрять между скроллом и подгрузкой —
    // тогда lastLogsCall() возвращал бы его и тест падал на before_id
    // (ловилось только в CI, локально прогон укладывался в интервал рефетча).
    await waitFor(() => expect(logsCalls.some((c) => c.before_id !== undefined)).toBe(true));
    const next = logsCalls.filter((c) => c.before_id !== undefined).at(-1)!;
    expect(next.status).toBe("err");
    expect(next.before_id).toBeDefined();
    expect(next.to).toBeDefined();
  });
});
