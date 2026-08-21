import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import RejectedTab from "./RejectedTab";
import { type RejectedGroup } from "../../lib/rejected";

// §94.7: вкладка «Логи → Отказы». Проверяем то, чего не видит бэкенд: что
// журнал отличает «отказов не было» от «сбор выключен», что фильтры доезжают
// до запроса, и что колонка команды показывается только администратору.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

// Подтверждение массовой отметки — сразу «да»: сам диалог проверяется тестами
// ConfirmProvider, здесь важно, что кнопка доходит до запроса.
const { confirmMock } = vi.hoisted(() => ({ confirmMock: vi.fn(async () => true) }));
vi.mock("../../lib/confirm", () => ({ useConfirm: () => confirmMock }));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function group(over: Partial<RejectedGroup> = {}): RejectedGroup {
  return {
    id: "g1",
    team_slug: "vika",
    team_name: "Вика",
    node_path: "telephony",
    reason: "node_not_found",
    http_method: "POST",
    status: 404,
    first_seen: "2026-08-19T09:12:00Z",
    last_seen: "2026-08-20T12:41:00Z",
    count: 842,
    clients: 3,
    ...over,
  };
}

type ServerOpts = {
  role?: string;
  groups?: RejectedGroup[];
  collecting?: boolean;
  retentionDays?: number;
  unresolved?: number;
  // scopeUnresolved — ответ сводки БЕЗ фильтров (вся область видимости). Она
  // отличается от экранной, когда непросмотренные лежат за пределами периода.
  scopeUnresolved?: number;
};

function mockServer(o: ServerOpts = {}) {
  const groups = o.groups ?? [group()];
  const collecting = o.collecting ?? true;
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u1", role: o.role ?? "operator" } });
    }
    if (url === "/api/rejected") {
      return Promise.resolve({
        groups,
        total: groups.length,
        retention_days: o.retentionDays ?? 30,
        collecting,
      });
    }
    if (url === "/api/rejected/summary") {
      // Без второго аргумента запрашивается сводка всей области видимости —
      // ею меряется доступность «Пометить все» (§94.6).
      const scoped = params === undefined;
      return Promise.resolve({
        count: 842,
        groups: groups.length,
        clients: 3,
        unresolved: scoped ? (o.scopeUnresolved ?? o.unresolved ?? 1) : (o.unresolved ?? 1),
        retention_days: o.retentionDays ?? 30,
        collecting,
      });
    }
    if (url === "/api/me/prefs") return Promise.resolve({ items: [] });
    if (url === "/api/me/teams") {
      return Promise.resolve({ items: [{ id: "team-1", slug: "vika", name: "Вика" }] });
    }
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
  return render(<RejectedTab />, { wrapper });
}

describe("RejectedTab", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
    confirmMock.mockClear();
  });

  it("показывает группы отказов", async () => {
    mockServer();
    renderTab();

    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());
    // Ищем внутри таблицы: та же подпись причины есть и в выпадающем фильтре.
    const table = within(screen.getByRole("table"));
    expect(table.getByText("rejected.reason.node_not_found")).toBeInTheDocument();
    expect(table.getByText("404")).toBeInTheDocument();
  });

  it("колонка команды — только у администратора", async () => {
    mockServer({ role: "admin" });
    const { unmount } = renderTab();
    await waitFor(() => expect(screen.getByText("rejected.columns.team")).toBeInTheDocument());
    unmount();

    apiGet.mockReset();
    mockServer({ role: "operator" });
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());
    expect(screen.queryByText("rejected.columns.team")).not.toBeInTheDocument();
  });

  it("отличает «отказов нет» от «журнал выключен»", async () => {
    mockServer({ groups: [], collecting: true });
    const { unmount } = renderTab();
    await waitFor(() => expect(screen.getByText("rejected.empty")).toBeInTheDocument());
    expect(screen.queryByText("rejected.disabled_hint")).not.toBeInTheDocument();
    unmount();

    apiGet.mockReset();
    mockServer({ groups: [], collecting: false, retentionDays: 0 });
    renderTab();
    await waitFor(() => expect(screen.getByText("rejected.empty_disabled")).toBeInTheDocument());
    // Выключенный журнал объясняется прямо на экране и даёт путь к настройке —
    // иначе пустая таблица читается как «всё спокойно».
    expect(screen.getByText("rejected.disabled_hint")).toBeInTheDocument();
    expect(screen.getByText("rejected.disabled_link")).toHaveAttribute("href", "/settings/general");
  });

  it("фильтр причины уезжает в запрос", async () => {
    mockServer();
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());

    fireEvent.change(screen.getByRole("combobox"), { target: { value: "rate_limited" } });

    await waitFor(() => {
      const call = apiGet.mock.calls.find(
        ([url, params]) => url === "/api/rejected" && params?.reasons === "rate_limited",
      );
      expect(call).toBeTruthy();
    });
  });

  it("«Пометить все» спрашивает подтверждение и шлёт resolve-all", async () => {
    mockServer();
    apiPost.mockResolvedValue({ marked: 3 });
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());

    fireEvent.click(screen.getByText("rejected.resolve_all.title"));

    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/rejected/resolve-all", {}),
    );
  });

  it("«Пометить все» неактивна, когда непросмотренных нет", async () => {
    mockServer({ unresolved: 0 });
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());

    const btn = screen.getByText("rejected.resolve_all.title").closest("button");
    expect(btn).toBeDisabled();
  });

  it("«Пометить все» активна, когда непросмотренные лежат вне периода экрана", async () => {
    // Экран (узкий период) непросмотренных не показывает, а в области
    // видимости они есть — бейдж в сайдбаре горит. Кнопка обязана оставаться
    // рабочей: она гасит счётчик целиком, а не то, что попало в период.
    mockServer({ unresolved: 0, scopeUnresolved: 3 });
    apiPost.mockResolvedValue({ marked: 3 });
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());

    const btn = screen.getByText("rejected.resolve_all.title").closest("button");
    await waitFor(() => expect(btn).toBeEnabled());

    fireEvent.click(screen.getByText("rejected.resolve_all.title"));
    await waitFor(() => expect(apiPost).toHaveBeenCalledWith("/api/rejected/resolve-all", {}));
  });

  it("ссылка выгрузки повторяет фильтры экрана", async () => {
    mockServer();
    renderTab();
    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());

    const link = screen.getByText("rejected.export_csv").closest("a");
    expect(link).toBeTruthy();
    const href = link!.getAttribute("href") ?? "";
    // Период обязан быть в ссылке: CSV без него отдал бы другое множество
    // групп, чем показано в таблице.
    expect(href).toContain("/api/rejected/export.csv?");
    expect(href).toContain("from=");
    expect(href).toContain("to=");
  });
});
