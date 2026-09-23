import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §107: форма узла. «Не открывать поток (SSE) на GET» — только у sync-узла
// (карточка «Маршрут»), «Не считать таймаут отказом защиты» — у всех типов
// (карточка «Куда перенаправить»). Оба флага уходят в тело сохранения; при
// смене типа на async SSE-флаг отправляется выключенным, чтобы сервер не писал
// в аудит «сброшено несовместимое поле» за переключатель, которого не видно.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

vi.mock("../lib/nodeShare", async () => {
  const actual = await vi.importActual<typeof import("../lib/nodeShare")>("../lib/nodeShare");
  return { ...actual, useEnsureNodeTeam: () => ({ status: "ready" }) };
});

vi.mock("../lib/useCurrentRole", () => ({ useRoleAtLeast: () => true }));

vi.mock("../lib/confirm", async () => {
  const actual = await vi.importActual<typeof import("../lib/confirm")>("../lib/confirm");
  return { ...actual, useConfirm: () => () => Promise.resolve(true) };
});

import NodeSettings from "./NodeSettings";
import { TooltipProvider } from "../components/ui";

const NODE = {
  id: "n1",
  team_id: "t1",
  path: "mcp-1c-vika_dev5",
  root_method: "request",
  incoming_method: "ANY",
  outgoing_method: "ANY",
  url_mode: "static",
  target_url: "http://192.168.3.115:8172/mcp",
  status: "enabled",
  auth_type: "none",
  incoming_auth_type: "none",
  timeout_ms: 300000,
  clickhouse_table: "nexus_mcp.logs",
  sse_decline: true,
  breaker_ignore_timeouts: false,
};

function renderForm() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/nodes/n1/edit"]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <Routes>
            <Route path="/nodes/:id/edit" element={<NodeSettings />} />
            <Route path="/nodes/:id" element={<div>NODE-PAGE</div>} />
          </Routes>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const sseSwitch = () => screen.queryByRole("switch", { name: "node.form.sse_decline" });
const timeoutSwitch = () =>
  screen.getByRole("switch", { name: "node.form.breaker_ignore_timeouts" });

beforeEach(() => {
  apiGet.mockReset();
  apiPut.mockReset();
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1") return Promise.resolve(NODE);
    if (url === "/api/auth/me") return Promise.resolve({ user: { role: "admin" } });
    return Promise.resolve({ items: [] });
  });
  apiPut.mockResolvedValue(NODE);
});

describe("NodeSettings — флаги §107", () => {
  it("значения узла подставляются в переключатели", async () => {
    renderForm();
    await waitFor(() => expect(sseSwitch()).toHaveAttribute("aria-checked", "true"));
    expect(timeoutSwitch()).toHaveAttribute("aria-checked", "false");
  });

  it("оба флага уходят в тело сохранения", async () => {
    renderForm();
    await waitFor(() => expect(sseSwitch()).toHaveAttribute("aria-checked", "true"));
    fireEvent.click(timeoutSwitch());
    fireEvent.click(screen.getByRole("button", { name: /common\.save/ }));

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    const [url, payload] = apiPut.mock.calls[0];
    expect(url).toBe("/api/nodes/n1");
    expect(payload).toMatchObject({ sse_decline: true, breaker_ignore_timeouts: true });
  });

  it("у async-узла SSE-переключателя нет и флаг уходит выключенным", async () => {
    renderForm();
    await waitFor(() => expect(sseSwitch()).toHaveAttribute("aria-checked", "true"));
    fireEvent.click(screen.getByText("requestAsync"));

    expect(sseSwitch()).toBeNull();
    expect(timeoutSwitch()).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /common\.save/ }));
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(apiPut.mock.calls[0][1]).toMatchObject({ sse_decline: false });
  });
});
