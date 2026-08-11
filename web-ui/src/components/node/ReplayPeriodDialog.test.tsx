import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { QueueTab } from "./QueueTab";
import { ReplayPeriodDialog } from "./ReplayPeriodDialog";
import { type Node } from "../../api/client";
import { ConfirmContext } from "../../lib/confirm";

// §85: повторная отправка из логов за период.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}));

const ASYNC_NODE = {
  id: "n1",
  path: "demo/async",
  root_method: "requestAsync",
  clickhouse_table: "nexus_default.demo",
  logging_enabled: true,
  log_request_body: true,
  incoming_method: "POST",
} as unknown as Node;

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <ConfirmContext.Provider value={() => Promise.resolve(true)}>{children}</ConfirmContext.Provider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

function baseGet() {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    if (url.endsWith("/logs/failed-count")) {
      return Promise.resolve({ count: 0, logs_configured: true, logs_available: true });
    }
    if (url.endsWith("/async-queue/messages")) {
      return Promise.resolve({ items: [], capped: false, kafka_available: true });
    }
    return Promise.resolve({});
  });
}

const PLAN = {
  total: 40,
  scanned: 40,
  exact: true,
  eligible: 38,
  skipped_by: { body_truncated: 2 },
  eligibles: [
    {
      id: "a",
      date_request: "2026-08-05T10:00:00Z",
      http_method: "POST",
      method: "",
      url: "https://x/y",
      status: 200,
      done: true,
      request_size: 120,
    },
  ],
  ineligibles: [
    {
      id: "b",
      date_request: "2026-08-05T10:01:00Z",
      http_method: "POST",
      method: "",
      url: "https://x/y",
      status: 200,
      done: true,
      request_size: 999999,
      skip_reason: "body_truncated",
    },
  ],
  from: "2026-08-05T00:00:00Z",
  to: "2026-08-06T00:00:00Z",
};

describe("Кнопка «Отправить повторно из логов» на вкладке «Очередь»", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    baseGet();
  });

  it("недоступна, когда узел не логирует тело запроса", async () => {
    const node = { ...ASYNC_NODE, log_request_body: false } as Node;
    render(wrap(<QueueTab node={node} />));

    const btn = await screen.findByRole("button", { name: /queue\.replay_period/ });
    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute("title", "queue.replay_period_off_body");
  });

  it("недоступна, когда логирование узла выключено", async () => {
    const node = { ...ASYNC_NODE, logging_enabled: false } as Node;
    render(wrap(<QueueTab node={node} />));

    const btn = await screen.findByRole("button", { name: /queue\.replay_period/ });
    expect(btn).toBeDisabled();
    expect(btn).toHaveAttribute("title", "queue.replay_period_off_logs");
  });

  // §85.4: у sync-узла реинжекция упирается в гейт async-входа §82.3, поэтому
  // операции нет вовсе — а не «есть, но всегда падает».
  it("отсутствует у синхронного узла", async () => {
    const node = { ...ASYNC_NODE, root_method: "request" } as Node;
    render(wrap(<QueueTab node={node} />));

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /queue\.replay_period/ })).toBeNull();
  });

  it("доступна на async-узле с логированием тела", async () => {
    render(wrap(<QueueTab node={ASYNC_NODE} />));
    const btn = await screen.findByRole("button", { name: /queue\.replay_period/ });
    expect(btn).toBeEnabled();
  });
});

describe("Диалог повторной отправки (§85)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    baseGet();
  });

  it("«Проверить» шлёт plan и показывает обе группы с причинами", async () => {
    apiPost.mockResolvedValueOnce(PLAN);
    render(wrap(<ReplayPeriodDialog node={ASYNC_NODE} onClose={() => {}} />));

    fireEvent.click(screen.getByRole("button", { name: /replay_period\.check/ }));

    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(1));
    const [url, body, opts] = apiPost.mock.calls[0];
    expect(url).toBe("/api/nodes/n1/logs/replay-period/plan");
    expect(body).toEqual({ skip_replay_copies: true });
    // Окно уходит query-параметрами — теми же, что у журнала (§72.2).
    expect(opts.params.from).toBeTruthy();
    expect(opts.params.to).toBeTruthy();

    expect(await screen.findByText("38")).toBeInTheDocument();
    expect(screen.getAllByText("replay_period.will_send").length).toBeGreaterThan(0);
    expect(screen.getByText(/replay_period\.reason\.body_truncated/)).toBeInTheDocument();
  });

  // §85.6: главная защита от самоусиления. Границы, показанные в предпросмотре,
  // обязаны уйти в КАЖДЫЙ батч; пересчёт «по сейчас» затянул бы в набор
  // собственные повторы прогона.
  it("прогон шлёт зафиксированные границы окна и идёт по курсору до конца", async () => {
    apiPost
      .mockResolvedValueOnce(PLAN)
      .mockResolvedValueOnce({
        scanned: 20,
        replayed: 20,
        failed: 0,
        skipped_by: {},
        next_cursor: { after_ms: 1754388000000, after_id: "a" },
      })
      .mockResolvedValueOnce({ scanned: 18, replayed: 18, failed: 0, skipped_by: {}, next_cursor: null });

    render(wrap(<ReplayPeriodDialog node={ASYNC_NODE} onClose={() => {}} />));
    fireEvent.click(screen.getByRole("button", { name: /replay_period\.check/ }));
    await screen.findByText("38");
    fireEvent.click(screen.getByRole("button", { name: /replay_period\.send/ }));

    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(3));

    const [, body1, opts1] = apiPost.mock.calls[1];
    const [, body2, opts2] = apiPost.mock.calls[2];
    expect(opts1.params.from).toBe(PLAN.from);
    expect(opts1.params.to).toBe(PLAN.to);
    expect(opts2.params.from).toBe(PLAN.from);
    expect(opts2.params.to).toBe(PLAN.to);
    expect(body1.cursor).toBeNull();
    expect(body2.cursor).toEqual({ after_ms: 1754388000000, after_id: "a" });

    expect(await screen.findByText(/replay_period\.done/)).toBeInTheDocument();
  });

  it("ошибка батча показывается и цикл прекращается", async () => {
    apiPost
      .mockResolvedValueOnce(PLAN)
      .mockRejectedValueOnce({ response: { data: { error: "boom" } } });

    render(wrap(<ReplayPeriodDialog node={ASYNC_NODE} onClose={() => {}} />));
    fireEvent.click(screen.getByRole("button", { name: /replay_period\.check/ }));
    await screen.findByText("38");
    fireEvent.click(screen.getByRole("button", { name: /replay_period\.send/ }));

    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(apiPost).toHaveBeenCalledTimes(2);
  });

  it("кнопка отправки заблокирована, когда отправлять нечего", async () => {
    // eligibles/ineligibles НЕ переданы: Go отдаёт пустой срез как null, и
    // диалог обязан это пережить, а не падать на самом безобидном случае.
    apiPost.mockResolvedValueOnce({ ...PLAN, eligible: 0, eligibles: undefined, ineligibles: undefined });
    render(wrap(<ReplayPeriodDialog node={ASYNC_NODE} onClose={() => {}} />));

    fireEvent.click(screen.getByRole("button", { name: /replay_period\.check/ }));
    await screen.findByText("replay_period.empty");
    expect(screen.getByRole("button", { name: /replay_period\.send/ })).toBeDisabled();
  });
});
