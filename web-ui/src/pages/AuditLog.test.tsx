import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AuditLog from "./AuditLog";

// §91: до этой правки страница просила limit=200 одним запросом без пагинации,
// поэтому записи за пределами первых двухсот были недостижимы, а счётчика,
// который показал бы обрезку, не существовало. Тесты фиксируют подгрузку по
// скроллу с keyset-курсором и счётчик «показано N из M».

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

// i18n в тестах не инициализирован — t() возвращает ключ; проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const PAGE_SIZE = 100;

let auditCalls: Record<string, unknown>[] = [];
let countCalls: Record<string, unknown>[] = [];

function entry(i: number) {
  return {
    id: `id-${i}`,
    user_login: "alice",
    action: "node.update",
    target_type: "node",
    target_id: "00000000-0000-0000-0000-000000000001",
    ip_address: "10.0.0.1",
    team_id: "t1",
    // Метки убывают — курсор берётся из последней строки страницы.
    created_at: new Date(Date.UTC(2026, 7, 17, 12, 0, 0) - i * 60_000).toISOString(),
  };
}

// mockServer — total записей, страницами по PAGE_SIZE.
function mockServer(total: number) {
  auditCalls = [];
  countCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/audit") {
      const p = params ?? {};
      auditCalls.push(p);
      const from = (auditCalls.length - 1) * PAGE_SIZE;
      const size = Math.max(0, Math.min(PAGE_SIZE, total - from));
      return Promise.resolve({ items: Array.from({ length: size }, (_, i) => entry(from + i)) });
    }
    if (url === "/api/audit/count") {
      countCalls.push(params ?? {});
      return Promise.resolve({ count: total });
    }
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u1", role: "admin", current_team_id: "t1" } });
    }
    if (url === "/api/me/teams") {
      // current_team_id живёт именно здесь (useCurrentTeamID читает этот ответ),
      // и без него список остаётся выключенным — запросов не будет вовсе.
      return Promise.resolve({
        items: [{ id: "t1", name: "Default", slug: "default" }],
        current_team_id: "t1",
      });
    }
    return Promise.resolve({});
  });
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<AuditLog />, { wrapper });
}

// jsdom не считает layout, поэтому размеры контейнера задаём вручную — и делаем
// это ДО прихода данных: при нулевой высоте сработала бы автодогрузка «страница
// короче контейнера» и подменила проверяемый сценарий.
// rowCount — строки именно таблицы: getAllByText("node.update") цепляет ещё и
// <option> из выпадающего фильтра действий.
function rowCount() {
  return document.querySelectorAll("tbody tr").length;
}

function stubScrollBox() {
  const wrap = document.querySelector<HTMLDivElement>(".overflow-y-auto");
  expect(wrap).not.toBeNull();
  Object.defineProperty(wrap!, "scrollHeight", { value: 5000, configurable: true });
  Object.defineProperty(wrap!, "clientHeight", { value: 500, configurable: true });
  Object.defineProperty(wrap!, "scrollTop", { value: 0, writable: true, configurable: true });
  return wrap!;
}

describe("AuditLog — подгрузка по скроллу (§91.1/§91.3)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer(250);
  });

  it("первый запрос идёт страницей, а не жёстким limit=200", async () => {
    renderPage();
    await waitFor(() => expect(auditCalls.length).toBeGreaterThan(0));

    expect(auditCalls[0].limit).toBe(PAGE_SIZE);
    expect(auditCalls[0].before_ts).toBeUndefined();
    expect(auditCalls[0].before_id).toBeUndefined();
  });

  it("подгрузка следующей страницы несёт keyset-курсор и сохраняет фильтр", async () => {
    renderPage();
    const wrap = stubScrollBox();
    await waitFor(() => expect(rowCount()).toBe(PAGE_SIZE));

    wrap.scrollTop = 4400;
    fireEvent.scroll(wrap);

    await waitFor(() => expect(auditCalls.length).toBe(2));
    const second = auditCalls[1];
    // Курсор — пара: только времени мало, метки не уникальны.
    expect(second.before_ts).toBe(entry(PAGE_SIZE - 1).created_at);
    expect(second.before_id).toBe(`id-${PAGE_SIZE - 1}`);
    expect(second.limit).toBe(PAGE_SIZE);
    await waitFor(() => expect(rowCount()).toBe(PAGE_SIZE * 2));
  });

  it("недобор страницы означает конец истории", async () => {
    mockServer(30); // меньше одной страницы
    renderPage();
    stubScrollBox();

    await waitFor(() => expect(rowCount()).toBe(30));
    expect(await screen.findByText("audit.no_more")).toBeInTheDocument();
    // Вторая страница не запрашивается вовсе.
    expect(auditCalls.length).toBe(1);
  });

  it("счётчик показывает «из скольких» и не получает limit", async () => {
    renderPage();
    await waitFor(() => expect(countCalls.length).toBeGreaterThan(0));

    expect(countCalls[0].limit).toBeUndefined();
    expect(countCalls[0].before_ts).toBeUndefined();
    expect(await screen.findByText("audit.shown_of_total")).toBeInTheDocument();
  });

  it("фильтр по действию уходит и в список, и в счётчик", async () => {
    render(
      <MemoryRouter initialEntries={["/audit?action=node.delete"]}>
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <AuditLog />
        </QueryClientProvider>
      </MemoryRouter>,
    );

    await waitFor(() => expect(auditCalls.length).toBeGreaterThan(0));
    await waitFor(() => expect(countCalls.length).toBeGreaterThan(0));
    // Оба множества обязаны совпадать, иначе «показано N из M» врёт.
    expect(auditCalls[0].action).toBe("node.delete");
    expect(countCalls[0].action).toBe("node.delete");
  });
});
