import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §113.11: кнопки статуса вкладки «Очередь». Пауза sync-узла спрашивает
// подтверждение (он не отклоняет запросы, а копит их с 202); выключение
// спрашивает всегда и называет число ожидающих с плитки. Красное на коде до
// правки: пауза уходила без диалога, а текст выключения был одним на всех.

const { apiGet, apiPatch } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPatch: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, patch: apiPatch } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: { waiting?: string }) => (o?.waiting !== undefined ? `${k}:${o.waiting}` : k),
    i18n: { language: "ru" },
  }),
}));

vi.mock("../../lib/useCurrentRole", () => ({ useRoleAtLeast: () => true }));

import { QueueTab } from "./QueueTab";
import { ConfirmProvider } from "../ui/ConfirmProvider";
import { type Node } from "../../api/client";

function node(rootMethod: string, status: string): Node {
  return {
    id: "n1",
    path: "edo",
    team_id: "t1",
    root_method: rootMethod,
    status,
    clickhouse_table: "nexus.edo",
  } as unknown as Node;
}

let queueResp: unknown = { items: [], capped: false, kafka_available: true };

function renderTab(n: Node) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/nodes/n1?tab=queue"]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <ConfirmProvider>
            <QueueTab node={n} />
          </ConfirmProvider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const items = (n: number) =>
  Array.from({ length: n }, (_, i) => ({
    id: `m${i}`,
    partition: 0,
    offset: i,
    method: "POST",
    target_url: "http://x",
    received_at: "2026-10-06T10:00:00Z",
    body_size: 10,
  }));

beforeEach(() => {
  apiGet.mockReset();
  apiPatch.mockReset();
  apiPatch.mockResolvedValue({});
  queueResp = { items: [], capped: false, kafka_available: true };
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/prefs") return Promise.resolve({ items: [] });
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.includes("/logs/failed-count")) return Promise.resolve({ count: 0, logs_available: true });
    if (url.includes("/async-queue/messages")) return Promise.resolve(queueResp);
    if (url.includes("/breaker")) return Promise.resolve({ configured: false });
    return Promise.resolve({ items: [] });
  });
});

describe("QueueTab: подтверждение паузы (§113.11)", () => {
  it("sync-узел: пауза только после подтверждения", async () => {
    renderTab(node("request", "enabled"));
    fireEvent.click(await screen.findByText("queue.banner.pause"));

    expect(await screen.findByText("node.status_confirm.pause_sync_title")).toBeInTheDocument();
    fireEvent.click(screen.getByText("common.cancel"));
    await waitFor(() => expect(screen.queryByText("node.status_confirm.pause_sync_title")).toBeNull());
    expect(apiPatch).not.toHaveBeenCalled();

    fireEvent.click(screen.getByText("queue.banner.pause"));
    fireEvent.click(await screen.findByText("node.status_confirm.pause_sync_action"));
    await waitFor(() => expect(apiPatch).toHaveBeenCalledWith("/api/nodes/n1/status", { status: "paused" }));
  });

  it("async-узел ставится на паузу без диалога", async () => {
    renderTab(node("requestAsync", "enabled"));
    fireEvent.click(await screen.findByText("queue.banner.pause"));

    await waitFor(() => expect(apiPatch).toHaveBeenCalledWith("/api/nodes/n1/status", { status: "paused" }));
    expect(screen.queryByText("node.status_confirm.pause_sync_title")).toBeNull();
  });
});

describe("QueueTab: подтверждение выключения (§113.11)", () => {
  it("число ожидающих — с плитки, «+» при усечении", async () => {
    queueResp = { items: items(50), capped: true, kafka_available: true };
    renderTab(node("request", "paused"));
    // Ждём, пока плитка покажет список: число в диалоге берётся из него.
    expect(await screen.findByText("50+")).toBeInTheDocument();

    fireEvent.click(screen.getByText("queue.banner.disable"));
    expect(
      await screen.findByText("node.status_confirm.disable_queue_message:50+"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByText("node.status_confirm.disable_action"));
    await waitFor(() => expect(apiPatch).toHaveBeenCalledWith("/api/nodes/n1/status", { status: "disabled" }));
  });

  it("очередь недоступна — «неизвестно», а не «пусто»", async () => {
    queueResp = { items: [], capped: false, kafka_available: false };
    renderTab(node("requestAsync", "enabled"));
    await waitFor(() =>
      expect(apiGet.mock.calls.some((c) => String(c[0]).includes("/async-queue/messages"))).toBe(true),
    );

    fireEvent.click(screen.getByText("queue.banner.disable"));
    expect(await screen.findByText("node.status_confirm.disable_unknown_message")).toBeInTheDocument();
    fireEvent.click(screen.getByText("common.cancel"));
    expect(apiPatch).not.toHaveBeenCalled();
  });

  it("у включённого sync-узла очереди нет — текст без удаления", async () => {
    renderTab(node("request", "enabled"));
    fireEvent.click(await screen.findByText("queue.banner.disable"));
    expect(await screen.findByText("node.status_confirm.disable_empty_message")).toBeInTheDocument();
  });
});
