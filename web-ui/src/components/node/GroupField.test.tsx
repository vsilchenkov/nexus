import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { GroupField } from "./GroupField";
import { type NodeGroup } from "../../api/client";

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost } };
});

// i18n в тестах не инициализирован — t() возвращает ключ; проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Роль управляется отдельно: пункт «Создать» виден только manager+ (§99.4).
const { roleAtLeast } = vi.hoisted(() => ({ roleAtLeast: vi.fn(() => true) }));
vi.mock("../../lib/useCurrentRole", () => ({ useRoleAtLeast: () => roleAtLeast() }));

function group(id: string, name: string, usage = 0): NodeGroup {
  return {
    id,
    name,
    description: "",
    sort_order: 10,
    usage_count: usage,
    created_by: "",
    updated_by: "",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
  };
}

const GROUPS = [group("g1", "1С Обмен", 4), group("g2", "Курьерские службы", 6)];

function renderField(value = "", onChange = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <GroupField value={value} onChange={onChange} />
    </QueryClientProvider>,
  );
  return onChange;
}

// openDropdown раскрывает комбобокс и дожидается поля поиска.
async function openDropdown() {
  fireEvent.click(await screen.findByRole("button", { name: "node.fields.group" }));
  return screen.findByRole("combobox");
}

describe("GroupField", () => {
  beforeEach(() => {
    apiGet.mockReset().mockResolvedValue({ items: GROUPS });
    apiPost.mockReset();
    roleAtLeast.mockReturnValue(true);
  });

  it("без выбранной группы показывает «Без группы»", async () => {
    renderField("");
    expect(await screen.findByText("node.group_combo.none")).toBeTruthy();
  });

  it("показывает имя выбранной группы", async () => {
    renderField("g2");
    expect(await screen.findByText("Курьерские службы")).toBeTruthy();
  });

  // Ключевое: имя выбранной группы берётся из ПОЛНОГО списка, а не из выдачи
  // поиска. Иначе ввод, отфильтровавший выбранную группу, менял бы подпись
  // кнопки на «Без группы» — узел выглядел бы отвязанным.
  it("сохраняет подпись, когда поиск не вернул выбранную группу", async () => {
    apiGet.mockImplementation((_url: string, params?: { q?: string }) =>
      Promise.resolve({ items: params?.q ? [GROUPS[0]] : GROUPS }),
    );
    renderField("g2");
    const input = await openDropdown();
    fireEvent.change(input, { target: { value: "1С" } });
    // Оба условия в одном waitFor: по отдельности каждое выполняется и в
    // промежуточных состояниях (полный список / пустой во время загрузки).
    await waitFor(() => {
      expect(screen.getByText("1С Обмен")).toBeTruthy();
      expect(screen.getAllByText("Курьерские службы")).toHaveLength(1);
    });
    // Единственное оставшееся вхождение — подпись кнопки-триггера.
    expect(screen.getByRole("button", { name: "node.fields.group" }).textContent).toContain(
      "Курьерские службы",
    );
  });

  it("выбор группы отдаёт её id", async () => {
    const onChange = renderField("");
    await openDropdown();
    fireEvent.click(await screen.findByText("1С Обмен"));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("g1"));
  });

  it("«Без группы» снимает привязку", async () => {
    const onChange = renderField("g1");
    await openDropdown();
    // Первый «none» — подпись кнопки-триггера у пустого значения; здесь
    // значение задано, поэтому единственный такой текст — пункт списка.
    fireEvent.click(await screen.findByText("node.group_combo.none"));
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(""));
  });

  // Пункт «Создать» дёргает POST /api/node-groups, закрытый для роли ниже
  // manager: показывать его viewer'у значило бы обещать 403.
  it("пункт «Создать» скрыт для роли ниже manager", async () => {
    roleAtLeast.mockReturnValue(false);
    apiGet.mockResolvedValue({ items: [] });
    renderField("");
    const input = await openDropdown();
    fireEvent.change(input, { target: { value: "Новая" } });
    await waitFor(() => expect(screen.getByText("node.group_combo.empty")).toBeTruthy());
    expect(screen.queryByText("node.group_combo.create")).toBeNull();
  });

  // Создание из комбобокса — та самая ветка, ради которой POST сделан
  // идемпотентным: пользователь не разбирает конфликты, а получает группу.
  it("создание из списка шлёт POST и сразу выбирает созданную группу", async () => {
    apiGet.mockResolvedValue({ items: [] });
    apiPost.mockResolvedValue(group("g-new", "Новая"));
    const onChange = renderField("");
    const input = await openDropdown();
    fireEvent.change(input, { target: { value: "  Новая  " } });
    fireEvent.click(await screen.findByText("node.group_combo.create"));

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/node-groups", { name: "Новая" }),
    );
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("g-new"));
  });

  it("менеджеру предлагается создать группу с введённым именем", async () => {
    apiGet.mockResolvedValue({ items: [] });
    renderField("");
    const input = await openDropdown();
    fireEvent.change(input, { target: { value: "Новая" } });
    expect(await screen.findByText("node.group_combo.create")).toBeTruthy();
  });
});
