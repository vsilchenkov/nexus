import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogsTab } from "./LogsTab";
import { type LogRow } from "./types";
import { type Node } from "../../api/client";

// §98.6: журнал виртуализован, и потолок накопления поднят с 1000 до 50 000.
//
// Прежний потолок §44.K защищал DOM: без виртуализации тысяча строк по восемь
// ячеек вешала прокрутку. С фильтром это превращалось в стену — «Показано 1000
// из 47 000», и остальные совпадения были недостижимы. Проверяется и то, и
// другое: список уходит дальше тысячи записей, а в DOM их столько не попадает.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));
vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: vi.fn() } };
});
vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o?.n === undefined ? k : `${k}:${o.n}`),
    i18n: { language: "ru" },
  }),
}));

const PAGE_SIZE = 50;
const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika" } as Node;

function loadedCount(): number {
  const el = screen.getByText(/^logs\.shown_(count|of_total):/);
  return Number(el.textContent!.split(":")[1]);
}

let pages = 0;

function row(i: number): LogRow {
  return {
    id: `id-${i}`,
    url: "http://example.test/api",
    http_method: "POST",
    method: `m.${i}`,
    status: 0,
    duration_ms: 50000,
    date_request: new Date(Date.UTC(2026, 6, 21, 10, 0, 0) - i * 60_000).toISOString(),
    done: false,
  };
}

function mockServer() {
  pages = 0;
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1/logs") {
      const page = Array.from({ length: PAGE_SIZE }, (_, i) => row(pages * PAGE_SIZE + i));
      pages++;
      return Promise.resolve({ items: page, logs_available: true });
    }
    if (url === "/api/nodes/n1/logs/count") {
      return Promise.resolve({ total: 47000, logs_available: true });
    }
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    return Promise.resolve({});
  });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<LogsTab node={NODE} initialFilter={{ status: "err" }} />, { wrapper });
}

describe("LogsTab — прокрутка дальше тысячи записей (§98.6)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });

  it("с фильтром список уходит за прежний потолок в 1000 записей", async () => {
    renderTab();
    const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
    expect(wrap).not.toBeNull();
    Object.defineProperty(wrap!, "scrollHeight", { value: 5000, configurable: true });
    Object.defineProperty(wrap!, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(wrap!, "scrollTop", { value: 0, writable: true, configurable: true });

    await waitFor(() => expect(loadedCount()).toBe(PAGE_SIZE));

    // 22 страницы по 50 = 1100 записей: на старом потолке (1000) подгрузка
    // остановилась бы на 20-й, и счётчик замер бы на тысяче.
    for (let i = 2; i <= 22; i++) {
      wrap!.scrollTop = 4400;
      fireEvent.scroll(wrap!);
      await waitFor(() => expect(loadedCount()).toBe(PAGE_SIZE * i));
    }

    expect(loadedCount()).toBe(1100);
    // Подсказка «показаны первые N» на этом объёме не должна появляться вовсе.
    expect(screen.queryByText(/^logs\.cap_reached/)).toBeNull();
  }, 30000);

  it("в DOM живут только видимые строки, а не весь накопленный список", async () => {
    renderTab();
    const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
    Object.defineProperty(wrap!, "scrollHeight", { value: 5000, configurable: true });
    Object.defineProperty(wrap!, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(wrap!, "scrollTop", { value: 0, writable: true, configurable: true });

    await waitFor(() => expect(loadedCount()).toBe(PAGE_SIZE));
    for (let i = 2; i <= 6; i++) {
      wrap!.scrollTop = 4400;
      fireEvent.scroll(wrap!);
      await waitFor(() => expect(loadedCount()).toBe(PAGE_SIZE * i));
    }

    // 300 записей загружено — отрисовано кратно меньше. Ради этого
    // виртуализация и вводилась: иначе потолок пришлось бы вернуть.
    expect(loadedCount()).toBe(300);
    expect(screen.getAllByText(/^m\.\d+$/).length).toBeLessThan(100);
  }, 30000);
});
