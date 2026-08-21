import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RejectedDrawer } from "./RejectedDrawer";
import { type RejectedDetails } from "../../lib/rejected";

// §94.7: карточка группы отказов. Проверяем то, что не видит ни бэкенд, ни
// типизация: отметка «просмотрено» ставится САМИМ открытием (и ровно один раз),
// уже просмотренная группа запроса не делает, а сэмпл без тела пишет «без тела»
// вместо «тело —».

const { apiGet, apiPost, apiDel } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDel: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost, del: apiDel } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function details(over: Partial<RejectedDetails["group"]> = {}, bodyBytes = 0): RejectedDetails {
  return {
    group: {
      id: "g1",
      team_slug: "vika",
      team_name: "Вика",
      node_path: "telephony",
      reason: "node_not_found",
      http_method: "POST",
      status: 404,
      first_seen: "2026-08-19T09:12:00Z",
      last_seen: "2026-08-20T12:41:00Z",
      count: 5,
      clients: 1,
      ...over,
    },
    clients: [
      {
        ip: "10.0.0.1",
        host: "crm.example.ru",
        user_agent: "axios/1.6",
        count: 5,
        first_seen: "2026-08-19T09:12:00Z",
        last_seen: "2026-08-20T12:41:00Z",
      },
    ],
    samples: [
      {
        at: "2026-08-20T12:41:00Z",
        client_ip: "10.0.0.1",
        http_method: "GET",
        raw_path: "/api/v1/vika/telephony",
        status: 404,
        body_bytes: bodyBytes,
        request_id: "24c9afe6-6e71-471c-9a25-7323d51770dc",
        headers: {},
      },
    ],
  };
}

function renderDrawer(data: RejectedDetails, onViewed = vi.fn()) {
  apiGet.mockResolvedValue(data);
  apiPost.mockResolvedValue({});
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(
    <RejectedDrawer id="g1" onClose={vi.fn()} onViewed={onViewed} onDeleted={vi.fn()} />,
    { wrapper },
  );
  return { view, onViewed };
}

describe("RejectedDrawer", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
    apiDel.mockReset();
  });

  it("помечает группу просмотренной при открытии — ровно один раз", async () => {
    const { onViewed } = renderDrawer(details());

    await waitFor(() => expect(apiPost).toHaveBeenCalledWith("/api/rejected/g1/resolve", {}));
    await waitFor(() => expect(onViewed).toHaveBeenCalled());
    expect(onViewed.mock.calls[0][0]).toBe("g1");
    // Повторных отправок быть не должно: панель перерисовывается на каждый
    // тик автообновления счётчиков.
    expect(apiPost.mock.calls.filter(([url]) => url === "/api/rejected/g1/resolve")).toHaveLength(1);
  });

  it("уже просмотренную группу не помечает повторно", async () => {
    const { onViewed } = renderDrawer(details({ resolved_at: "2026-08-20T13:00:00Z" }));

    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());
    expect(apiPost).not.toHaveBeenCalled();
    expect(onViewed).not.toHaveBeenCalled();
  });

  it("ручной кнопки «пометить» больше нет, удаление осталось", async () => {
    renderDrawer(details());

    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());
    expect(screen.queryByText("rejected.details.resolve")).not.toBeInTheDocument();
    expect(screen.getByText("rejected.details.delete")).toBeInTheDocument();
  });

  it("сэмпл без тела пишет «без тела», а request_id подписан", async () => {
    renderDrawer(details());

    await waitFor(() => expect(screen.getByText("rejected.details.no_body")).toBeInTheDocument());
    expect(screen.getByText("rejected.details.request_id")).toBeInTheDocument();
    expect(screen.getByText("24c9afe6-6e71-471c-9a25-7323d51770dc")).toBeInTheDocument();
  });

  it("сэмпл с телом показывает размер", async () => {
    renderDrawer(details({}, 2048));

    await waitFor(() => expect(screen.getByText("rejected.details.body_bytes")).toBeInTheDocument());
    expect(screen.queryByText("rejected.details.no_body")).not.toBeInTheDocument();
  });

  it("панель не модальная: затемняющей подложки нет", async () => {
    const { view } = renderDrawer(details());

    await waitFor(() => expect(screen.getByText("telephony")).toBeInTheDocument());
    // Подложка перехватывала бы клики по списку, и переключение между строками
    // требовало бы закрытия карточки (§94.7).
    expect(view.container.querySelector(".bg-black\\/40")).toBeNull();
    expect(view.container.querySelector("aside")).not.toBeNull();
  });
});
