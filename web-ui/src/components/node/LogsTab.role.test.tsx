import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogsTab } from "./LogsTab";
import { type Node } from "../../api/client";
import { type LogRow } from "./types";

// §87: кнопка повтора записи в логах — operator+. Раньше гейт стоял на manager,
// и роль «Оператор» получала бы disabled-кнопку при живом бэкендовом доступе.
// Тест красный на прежнем коде.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const NODE = { id: "n1", clickhouse_table: "nexus_task.task_vika" } as Node;

function row(): LogRow {
  return {
    id: "id-1",
    url: "http://example.test/api",
    http_method: "POST",
    method: "task.getFiles",
    status: 200,
    duration_ms: 12,
    date_request: "2026-08-12T10:00:00Z",
    done: true,
  };
}

function mockServer(role: string) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1/logs") {
      return Promise.resolve({ items: [row()], logs_available: true });
    }
    if (url === "/api/nodes/n1/logs/count") {
      return Promise.resolve({ total: 1, logs_available: true });
    }
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u1", role } });
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
  return render(<LogsTab node={NODE} />, { wrapper });
}

// Кнопка повтора рендерится всегда — различается только disabled и tooltip
// (§58: скрывать не стали, чтобы было видно, что действие существует).
async function replayButton() {
  return await waitFor(() => {
    const btn = document.querySelector<HTMLButtonElement>(
      'button[title="node.actions.replay"], button[title="common.no_permission"]',
    );
    expect(btn).not.toBeNull();
    return btn!;
  });
}

describe("LogsTab — доступ к повтору записи (§87)", () => {
  beforeEach(() => apiGet.mockReset());

  it("у оператора кнопка повтора активна", async () => {
    mockServer("operator");
    renderTab();

    const btn = await replayButton();
    expect(btn).not.toBeDisabled();
    expect(btn.title).toBe("node.actions.replay");
  });

  it("у наблюдателя кнопка повтора заблокирована с подсказкой «нет прав»", async () => {
    mockServer("viewer");
    renderTab();

    await screen.findByText("task.getFiles");
    const btn = await replayButton();
    expect(btn).toBeDisabled();
    expect(btn.title).toBe("common.no_permission");
  });
});
