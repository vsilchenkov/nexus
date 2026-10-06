import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §113.11: смена статуса в форме узла применяется на «Сохранить», и там же
// спрашивается подтверждение: пауза sync-узла (запросы копятся с 202, а не
// отклоняются) и выключение узла с непустой очередью (число — из свежего
// списка очереди). Инцидент 06.10.2026 начался с паузы, поставленной в
// ожидании отказа. Красное на коде до правки: сохранение уходило сразу.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: { waiting?: string }) => (o?.waiting !== undefined ? `${k}:${o.waiting}` : k),
    i18n: { language: "ru" },
  }),
}));

vi.mock("../lib/nodeShare", async () => {
  const actual = await vi.importActual<typeof import("../lib/nodeShare")>("../lib/nodeShare");
  return { ...actual, useEnsureNodeTeam: () => ({ status: "ready" }) };
});

vi.mock("../lib/useCurrentRole", () => ({ useRoleAtLeast: () => true }));

import NodeSettings from "./NodeSettings";
import { ConfirmProvider, TooltipProvider } from "../components/ui";

const BASE = {
  id: "n1",
  team_id: "t1",
  path: "vika_dev-epd_sbis_pp",
  incoming_method: "POST",
  outgoing_method: "POST",
  url_mode: "static",
  target_url: "http://192.168.3.115:8172/epd",
  auth_type: "none",
  incoming_auth_type: "none",
  timeout_ms: 30000,
  clickhouse_table: "nexus_edo.logs",
};

let nodeResp: Record<string, unknown> = {};
let queue: () => Promise<unknown> = () => Promise.resolve({ items: [], capped: false, kafka_available: true });

function queueCalls() {
  return apiGet.mock.calls.filter((c) => String(c[0]).includes("/async-queue/messages")).length;
}

function renderForm() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/nodes/n1/edit"]}>
      <QueryClientProvider client={qc}>
        <TooltipProvider>
          <ConfirmProvider>
            <Routes>
              <Route path="/nodes/:id/edit" element={<NodeSettings />} />
              <Route path="/nodes/:id" element={<div>NODE-PAGE</div>} />
            </Routes>
          </ConfirmProvider>
        </TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

async function pickStatusAndSave(label: string) {
  renderForm();
  // Форма заполнена из узла, когда в поле пути стоит его путь.
  await screen.findByDisplayValue(BASE.path);
  fireEvent.click(screen.getByText(label));
  fireEvent.click(screen.getByRole("button", { name: /common\.save/ }));
}

const save = () => screen.getByRole("button", { name: /common\.save/ });
// dialogCancel — «Отмена» именно диалога: у формы есть своя кнопка с тем же текстом.
const dialogCancel = () => within(screen.getByRole("dialog")).getByText("common.cancel");

beforeEach(() => {
  apiGet.mockReset();
  apiPut.mockReset();
  queue = () => Promise.resolve({ items: [], capped: false, kafka_available: true });
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/nodes/n1") return Promise.resolve(nodeResp);
    if (url.includes("/async-queue/messages")) return queue();
    if (url === "/api/auth/me") return Promise.resolve({ user: { role: "admin" } });
    return Promise.resolve({ items: [] });
  });
  apiPut.mockImplementation(() => Promise.resolve(nodeResp));
});

describe("NodeSettings: пауза sync-узла (§113.11)", () => {
  beforeEach(() => {
    nodeResp = { ...BASE, root_method: "request", status: "enabled" };
  });

  it("отмена диалога — узел не сохраняется", async () => {
    await pickStatusAndSave("⏸ node.status.paused");
    expect(await screen.findByText("node.status_confirm.pause_sync_title")).toBeInTheDocument();
    fireEvent.click(dialogCancel());
    await waitFor(() => expect(screen.queryByText("node.status_confirm.pause_sync_title")).toBeNull());
    expect(apiPut).not.toHaveBeenCalled();
  });

  it("подтверждение — сохраняется пауза", async () => {
    await pickStatusAndSave("⏸ node.status.paused");
    fireEvent.click(await screen.findByText("node.status_confirm.pause_sync_action"));
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(apiPut.mock.calls[0][1]).toMatchObject({ status: "paused" });
  });

  it("async-узел ставится на паузу без диалога", async () => {
    nodeResp = { ...BASE, root_method: "requestAsync", status: "enabled" };
    await pickStatusAndSave("⏸ node.status.paused");
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(screen.queryByText("node.status_confirm.pause_sync_title")).toBeNull();
  });
});

describe("NodeSettings: выключение узла с очередью (§113.11)", () => {
  it("число ожидающих — из свежего списка очереди", async () => {
    nodeResp = { ...BASE, root_method: "request", status: "paused" };
    queue = () =>
      Promise.resolve({ items: [{ id: "a" }, { id: "b" }], capped: false, kafka_available: true });
    await pickStatusAndSave("⏹ node.status.disabled");

    expect(
      await screen.findByText("node.status_confirm.disable_queue_message:2"),
    ).toBeInTheDocument();
    expect(queueCalls()).toBe(1);
    fireEvent.click(screen.getByText("node.status_confirm.disable_action"));
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(apiPut.mock.calls[0][1]).toMatchObject({ status: "disabled" });
  });

  it("пустая очередь — без диалога", async () => {
    nodeResp = { ...BASE, root_method: "requestAsync", status: "enabled" };
    await pickStatusAndSave("⏹ node.status.disabled");
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(queueCalls()).toBe(1);
    expect(screen.queryByText("node.status_confirm.disable_title")).toBeNull();
  });

  it("список очереди не пришёл — спрашиваем с «неизвестно»", async () => {
    nodeResp = { ...BASE, root_method: "requestAsync", status: "enabled" };
    queue = () => Promise.reject(new Error("kafka down"));
    await pickStatusAndSave("⏹ node.status.disabled");

    expect(await screen.findByText("node.status_confirm.disable_unknown_message")).toBeInTheDocument();
    fireEvent.click(dialogCancel());
    await waitFor(() => expect(save()).toBeInTheDocument());
    expect(apiPut).not.toHaveBeenCalled();
  });

  // Ревизия §113: пока список очереди грузится, кнопка заблокирована, а
  // повторный вход игнорируется — иначе второй диалог лёг бы поверх первого и
  // первый промис не разрешился бы никогда.
  it("двойной клик во время чтения очереди — один диалог и одно сохранение", async () => {
    nodeResp = { ...BASE, root_method: "requestAsync", status: "enabled" };
    let release: (v: unknown) => void = () => {};
    queue = () => new Promise((r) => (release = r));
    await pickStatusAndSave("⏹ node.status.disabled");
    await waitFor(() => expect(queueCalls()).toBe(1));
    expect(save()).toBeDisabled();
    fireEvent.click(save());

    release({ items: [{ id: "a" }], capped: false, kafka_available: true });
    expect(await screen.findByText("node.status_confirm.disable_queue_message:1")).toBeInTheDocument();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(queueCalls()).toBe(1);
    fireEvent.click(screen.getByText("node.status_confirm.disable_action"));
    await waitFor(() => expect(apiPut).toHaveBeenCalledTimes(1));
  });

  it("включённый sync-узел: очереди нет — ни запроса, ни диалога", async () => {
    nodeResp = { ...BASE, root_method: "request", status: "enabled" };
    await pickStatusAndSave("⏹ node.status.disabled");
    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(queueCalls()).toBe(0);
  });
});
