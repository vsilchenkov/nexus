import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TooltipProvider } from "@radix-ui/react-tooltip";

import { QueueTab } from "./QueueTab";
import { type Node } from "../../api/client";
import { ConfirmContext } from "../../lib/confirm";

// §96.8: итог «Повторить все сейчас» с числом пропущенных записей.
//
// Записи, чей оригинал в очереди недоступен, а копия в журнале обрезана, НЕ
// отправляются (иначе приёмник получит обрезок — боевой инцидент 31.08.2026).
// Они остаются в списке, и оператор обязан узнать об этом числом, а не по
// расхождению «повторено 12, а было 15».

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, vars?: Record<string, unknown>) =>
      vars ? `${k}:${Object.values(vars).join(",")}` : k,
  }),
}));

const NODE = {
  id: "n1",
  clickhouse_table: "nexus_support.it_upr_vika",
  root_method: "requestAsync",
  dlq_ttl_seconds: 86400,
} as Node;

function mockServer() {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1/logs") return Promise.resolve({ items: [], logs_available: true });
    if (url === "/api/nodes/n1/async-queue/messages") {
      return Promise.resolve({ items: [], capped: false, kafka_available: true });
    }
    if (url === "/api/nodes/n1/breaker") {
      return Promise.resolve({ state: "closed", failures: 0, threshold: 5 });
    }
    if (url === "/api/nodes/n1/logs/failed-count") {
      return Promise.resolve({ count: 15, logs_configured: true, logs_available: true });
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

describe("QueueTab — итог массового повтора (§96.8)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer();
  });

  it("показывает, сколько повторено и сколько пропущено из-за недоступного оригинала", async () => {
    apiPost.mockResolvedValue({
      total: 15,
      replayed: 12,
      failed: 0,
      capped: false,
      skipped_client_canceled: 0,
      skipped_original_unavailable: 3,
    });
    renderTab();

    const btn = await screen.findByRole("button", { name: /queue\.replay_failed_all/ });
    fireEvent.click(btn);

    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/queue\.replay_failed_result:12,15/)).toBeInTheDocument();
    expect(screen.getByText(/queue\.replay_failed_skipped_original:3/)).toBeInTheDocument();
  });

  it("без пропусков строка итога не обещает лишнего", async () => {
    apiPost.mockResolvedValue({
      total: 4,
      replayed: 4,
      failed: 0,
      capped: false,
      skipped_client_canceled: 0,
      skipped_original_unavailable: 0,
    });
    renderTab();

    fireEvent.click(await screen.findByRole("button", { name: /queue\.replay_failed_all/ }));

    expect(await screen.findByText(/queue\.replay_failed_result:4,4/)).toBeInTheDocument();
    expect(screen.queryByText(/replay_failed_skipped_original/)).not.toBeInTheDocument();
    expect(screen.queryByText(/replay_failed_errors/)).not.toBeInTheDocument();
  });
});
