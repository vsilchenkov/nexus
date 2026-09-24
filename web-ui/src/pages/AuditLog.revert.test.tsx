import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AuditLog from "./AuditLog";

// §108: отмена изменения из журнала видна только администратору; неактуальные
// строки подсвечены, у строки удалённого узла кнопки нет.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const NODE = "00000000-0000-0000-0000-000000000001";

function row(id: string, revert?: Record<string, unknown>) {
  return {
    id,
    user_login: "alice",
    action: "node.update",
    target_type: "node",
    target_id: NODE,
    team_id: "t1",
    details: { status: { before: "paused", after: "enabled" } },
    created_at: "2026-09-23T18:36:22Z",
    ...(revert ? { revert } : {}),
  };
}

let revertCalls: string[] = [];

function mockServer(role: string) {
  revertCalls = [];
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/audit") {
      return Promise.resolve({
        items: [
          row("stale", { state: "stale", revertible: true, overwritten: { status: "paused" } }),
          row("gone", { state: "gone", revertible: false }),
          row("actual", { state: "actual", revertible: true }),
          row("plain"),
        ],
      });
    }
    if (url === "/api/audit/count") return Promise.resolve({ count: 4 });
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role } });
    if (url === "/api/me/teams") {
      return Promise.resolve({ items: [{ id: "t1", name: "Default", slug: "default" }], current_team_id: "t1" });
    }
    if (url.startsWith("/api/audit/revert/")) {
      revertCalls.push(url);
      return Promise.resolve({
        entry: { id: "stale", action: "node.update", created_at: "2026-09-23T18:36:22Z", user_login: "alice" },
        node: { id: NODE, path: "svc/x", team_id: "t1", updated_at: "2026-09-24T10:00:00.123456Z" },
        state: "stale",
        moved: false,
        fields: [],
        dry_run: { code: "", field: "", not_held: [] },
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

// mainTable — таблица журнала. Селекторы вида "tbody tr" цепляли бы и строки
// вложенных таблиц diff в ячейке «Детали».
function mainTable(): HTMLTableElement {
  const table = document.querySelector<HTMLTableElement>("table");
  expect(table).not.toBeNull();
  return table!;
}

function tableRow(id: string): HTMLTableRowElement {
  const idx = ["stale", "gone", "actual", "plain"].indexOf(id);
  return mainTable().tBodies[0].rows[idx];
}

describe("AuditLog — отмена изменения (§108)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("администратор видит кнопку отмены только у отменяемых строк живых узлов", async () => {
    mockServer("admin");
    renderPage();
    await waitFor(() => expect(screen.getAllByLabelText("audit.revert.button")).toHaveLength(2));

    expect(tableRow("stale").querySelector("[aria-label='audit.revert.button']")).not.toBeNull();
    expect(tableRow("actual").querySelector("[aria-label='audit.revert.button']")).not.toBeNull();
    expect(tableRow("gone").querySelector("[aria-label='audit.revert.button']")).toBeNull();
    expect(tableRow("plain").querySelector("[aria-label='audit.revert.button']")).toBeNull();
  });

  it("неактуальная строка и удалённый узел подсвечены чипом и фоном", async () => {
    mockServer("admin");
    renderPage();
    expect(await screen.findByText("audit.state.stale")).toBeInTheDocument();
    expect(screen.getByText("audit.state.gone")).toBeInTheDocument();

    expect(tableRow("stale").className).toContain("bg-warn/5");
    expect(tableRow("gone").className).toContain("bg-warn/5");
    expect(tableRow("actual").className).not.toContain("bg-warn/5");
    // Перебитое «Стало» зачёркнуто, текущее значение — в подсказке.
    const struck = tableRow("stale").querySelector("s");
    expect(struck?.textContent).toBe("enabled");
    expect(struck?.parentElement?.getAttribute("title")).toBe("audit.diff.now");
  });

  it("оператор не видит колонку отмены", async () => {
    mockServer("operator");
    renderPage();
    await waitFor(() => expect(mainTable().tBodies[0].rows).toHaveLength(4));

    expect(screen.queryByLabelText("audit.revert.button")).toBeNull();
    expect(mainTable().tHead!.rows[0].cells).toHaveLength(6);
  });

  it("кнопка открывает диалог с предпросмотром этой строки", async () => {
    mockServer("admin");
    renderPage();
    const btn = await waitFor(() => {
      const b = tableRow("stale").querySelector<HTMLButtonElement>("[aria-label='audit.revert.button']");
      expect(b).not.toBeNull();
      return b!;
    });
    fireEvent.click(btn);

    await waitFor(() => expect(revertCalls).toEqual(["/api/audit/revert/stale"]));
    expect(await screen.findByText("audit.revert.title")).toBeInTheDocument();
  });
});
