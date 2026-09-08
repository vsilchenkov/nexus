import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { NodeGroupsPanel } from "./NodeGroups";
import { type NodeGroup } from "../../api/client";

const { apiGet, apiPost, apiDel } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDel: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost, patch: vi.fn(), del: apiDel } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Диалог подтверждения удаления — подтверждаем всегда: проверяем гейт кнопки,
// а не сам диалог.
vi.mock("../../lib/confirm", () => ({ useConfirm: () => () => Promise.resolve(true) }));

function group(id: string, name: string, order: number, usage: number): NodeGroup {
  return {
    id,
    name,
    description: "",
    sort_order: order,
    usage_count: usage,
    created_by: "admin",
    updated_by: "admin",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
  };
}

const GROUPS = [
  group("g1", "1С Обмен", 10, 4),
  group("g2", "Курьерские службы", 20, 0),
  group("g3", "Маркетплейсы", 30, 2),
];

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <NodeGroupsPanel />
    </QueryClientProvider>,
  );
}

// rowOf находит строку таблицы по имени группы.
function rowOf(name: string): HTMLElement {
  return screen.getByText(name).closest("tr") as HTMLElement;
}

describe("NodeGroupsPanel", () => {
  beforeEach(() => {
    apiGet.mockReset().mockResolvedValue({ items: GROUPS });
    apiPost.mockReset().mockResolvedValue(undefined);
    apiDel.mockReset().mockResolvedValue(undefined);
  });

  it("показывает группы с порядком и счётчиком узлов", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());
    expect(within(rowOf("1С Обмен")).getByText("10")).toBeInTheDocument();
    expect(within(rowOf("1С Обмен")).getByText("settings.groups.used")).toBeInTheDocument();
    expect(within(rowOf("Курьерские службы")).getByText("settings.groups.not_used")).toBeInTheDocument();
  });

  // Три уровня одной защиты (§99.8): кнопка, 409 на сервере, ON DELETE RESTRICT
  // в СУБД. Здесь проверяется первый — он единственный, который видит оператор.
  it("удаление используемой группы заблокировано, свободной — доступно", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());

    const used = within(rowOf("1С Обмен")).getByLabelText("common.delete");
    expect(used).toBeDisabled();

    const free = within(rowOf("Курьерские службы")).getByLabelText("common.delete");
    expect(free).not.toBeDisabled();
    fireEvent.click(free);
    await waitFor(() => expect(apiDel).toHaveBeenCalledWith("/api/node-groups/g2"));
  });

  it("стрелка «выше» выключена у первой строки, «ниже» — у последней", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());

    expect(within(rowOf("1С Обмен")).getByLabelText("settings.groups.move_up")).toBeDisabled();
    expect(within(rowOf("1С Обмен")).getByLabelText("settings.groups.move_down")).not.toBeDisabled();
    expect(within(rowOf("Маркетплейсы")).getByLabelText("settings.groups.move_down")).toBeDisabled();
  });

  it("стрелка отправляет направление сдвига", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("Маркетплейсы")).toBeInTheDocument());

    fireEvent.click(within(rowOf("Маркетплейсы")).getByLabelText("settings.groups.move_up"));
    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/node-groups/g3/move", { direction: "up" }),
    );
  });

  // Стрелки двигают по списку, а поиск его сужает: сдвиг относительно
  // невидимого соседа выглядел бы как «нажал, ничего не изменилось».
  it("при активном поиске стрелки выключены", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());

    fireEvent.change(screen.getByPlaceholderText("settings.groups.search"), {
      target: { value: "курь" },
    });
    await waitFor(() =>
      expect(within(rowOf("1С Обмен")).getByLabelText("settings.groups.move_down")).toBeDisabled(),
    );
  });

  it("создаёт группу с именем, описанием и порядком", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());

    fireEvent.click(screen.getByText("settings.groups.add"));
    const inputs = screen.getAllByRole("textbox");
    fireEvent.change(inputs[0], { target: { value: "  Новая  " } });
    fireEvent.change(inputs[1], { target: { value: "описание" } });
    fireEvent.change(inputs[2], { target: { value: "40" } });
    fireEvent.click(screen.getByText("settings.groups.dialog.create"));

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/node-groups", {
        name: "Новая",
        description: "описание",
        sort_order: 40,
      }),
    );
  });

  it("нечисловой порядок блокирует сохранение", async () => {
    renderPanel();
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());

    fireEvent.click(screen.getByText("settings.groups.add"));
    const inputs = screen.getAllByRole("textbox");
    fireEvent.change(inputs[0], { target: { value: "Новая" } });
    fireEvent.change(inputs[2], { target: { value: "не число" } });

    expect(screen.getByText("settings.groups.dialog.create").closest("button")).toBeDisabled();
    expect(screen.getByText("settings.groups.order_range")).toBeInTheDocument();
  });
});
