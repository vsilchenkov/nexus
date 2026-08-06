import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TooltipProvider } from "@radix-ui/react-tooltip";

import { BreakerCard, type BreakerState } from "./BreakerCard";
import { ConfirmContext } from "../../lib/confirm";

// §81.4: карточка защиты узла. Проверяется то, чего не видят типы и бэкенд-тесты:
// что уходит в сеть по клику, кому видна кнопка и прячется ли блок при
// деградации.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, opts?: Record<string, unknown>) =>
      opts?.value !== undefined ? `${k}:${String(opts.value)}` : k,
  }),
}));

const OPEN_STATE: BreakerState = {
  available: true,
  exists: true,
  state: "open",
  failures: 5,
  threshold: 5,
  opened_at: new Date().toISOString(),
  retry_after_ms: 12_000,
};

// Роль приходит из /api/auth/me — тем же запросом, что и в остальных вкладках.
function mockServer(state: BreakerState, role: "admin" | "viewer" = "admin") {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1/breaker") return Promise.resolve(state);
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role } });
    return Promise.resolve({});
  });
  apiPost.mockResolvedValue({
    was_open: true,
    previous_state: "open",
    previous_failures: 5,
    node_status_cleared: true,
  });
}

function renderCard() {
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
  return render(<BreakerCard nodeId="n1" />, { wrapper });
}

describe("BreakerCard", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
  });

  it("показывает открытое состояние со счётчиком и обратным отсчётом", async () => {
    mockServer(OPEN_STATE);
    renderCard();

    expect(await screen.findByText(/queue\.breaker\.state_open/)).toBeInTheDocument();
    expect(screen.getByText(/queue\.breaker\.failures:5\/5/)).toBeInTheDocument();
    expect(screen.getByText(/queue\.breaker\.retry_after:0:12/)).toBeInTheDocument();
  });

  it("кнопка сброса шлёт POST на /breaker/reset", async () => {
    mockServer(OPEN_STATE);
    renderCard();

    fireEvent.click(await screen.findByRole("button", { name: /queue\.breaker\.reset/ }));

    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    // Проверяем именно адрес: клик может «работать», уходя не туда (урок §79.11).
    expect(apiPost.mock.calls[0][0]).toBe("/api/nodes/n1/breaker/reset");
  });

  it("без Redis карточка не рендерится вовсе", async () => {
    mockServer({ ...OPEN_STATE, available: false });
    renderCard();

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(screen.queryByText(/queue\.breaker\./)).not.toBeInTheDocument();
  });

  it("наблюдатель видит состояние, но кнопки сброса у него нет", async () => {
    mockServer(OPEN_STATE, "viewer");
    renderCard();

    expect(await screen.findByText(/queue\.breaker\.state_open/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /queue\.breaker\.reset/ })).not.toBeInTheDocument();
  });

  // Ревизия §81.9: «3 из 5» — предупреждение «узел на грани». Раньше счётчик
  // рисовался только при открытой защите, где он всегда равен порогу, то есть
  // хранение политики в состоянии не давало ничего.
  it("показывает частичный счёт отказов при ещё закрытой защите", async () => {
    mockServer({
      ...OPEN_STATE,
      state: "closed",
      failures: 3,
      threshold: 5,
      retry_after_ms: 0,
    });
    renderCard();

    expect(await screen.findByText(/queue\.breaker\.state_closed/)).toBeInTheDocument();
    expect(screen.getByText(/queue\.breaker\.failures:3\/5/)).toBeInTheDocument();
  });

  it("закрытая защита показывается одной строкой без объяснений", async () => {
    mockServer({ ...OPEN_STATE, state: "closed", exists: false, failures: 0, retry_after_ms: 0 });
    renderCard();

    expect(await screen.findByText(/queue\.breaker\.state_closed/)).toBeInTheDocument();
    expect(screen.queryByText(/queue\.breaker\.explain_open/)).not.toBeInTheDocument();
  });
});
