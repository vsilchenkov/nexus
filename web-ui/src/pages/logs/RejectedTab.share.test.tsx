import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import RejectedTab from "./RejectedTab";
import { type RejectedGroup } from "../../lib/rejected";

// §98.3: карточка отказа обязана иметь адрес — иначе ею нельзя поделиться, и
// разбор приходится пересказывать словами («открой третью строку сверху»).
// Проверяется и обратное направление: ссылка ОТКРЫВАЕТ карточку, в том числе
// когда группы нет в текущей выдаче списка.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));
vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});
vi.mock("../../lib/confirm", () => ({ useConfirm: () => vi.fn(async () => true) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));

const { copyMock } = vi.hoisted(() => ({ copyMock: vi.fn(async (_url: string) => true) }));
vi.mock("../../lib/clipboard", () => ({ copyToClipboard: copyMock }));

const GROUP: RejectedGroup = {
  id: "g-42",
  team_slug: "vika",
  team_name: "Вика",
  node_path: "epd_sbis_pp-vika_dev/edo",
  reason: "unauthorized",
  http_method: "POST",
  status: 401,
  first_seen: "2026-09-07T08:27:33Z",
  last_seen: "2026-09-07T13:05:22Z",
  count: 351,
  clients: 1,
};

// listGroups — что отдаёт СПИСОК. Пустой список при непустой карточке — это
// «поделились ссылкой на отказ за пределами моего периода».
function mockServer(listGroups: RejectedGroup[]) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
    }
    if (url === "/api/rejected") {
      return Promise.resolve({
        groups: listGroups,
        total: listGroups.length,
        retention_days: 30,
        collecting: true,
      });
    }
    if (url === "/api/rejected/summary") {
      return Promise.resolve({
        count: 351,
        groups: listGroups.length,
        clients: 1,
        unresolved: 0,
        retention_days: 30,
        collecting: true,
      });
    }
    if (url === `/api/rejected/${GROUP.id}`) {
      return Promise.resolve({ group: GROUP, clients: [], samples: [] });
    }
    if (url === "/api/me/prefs") return Promise.resolve({ items: [] });
    if (url === "/api/me/teams") {
      return Promise.resolve({ items: [{ id: "team-1", slug: "vika", name: "Вика" }] });
    }
    return Promise.resolve({});
  });
}

// Зонд адреса: проверяем, что открытие карточки действительно уезжает в URL.
function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="loc">{loc.search}</div>;
}

function renderTab(initialUrl = "/logs/rejected") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[initialUrl]}>
      <QueryClientProvider client={qc}>
        {children}
        <LocationProbe />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<RejectedTab />, { wrapper });
}

describe("RejectedTab — ссылка на конкретный отказ (§98.3)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
    // Открытие карточки само помечает группу просмотренной (§94.7) — без
    // ответа на этот POST панель падает ещё до того, как дойдёт до проверки.
    apiPost.mockResolvedValue({});
    copyMock.mockClear();
  });

  it("адрес с ?rejected=<id> открывает карточку сразу при загрузке", async () => {
    mockServer([GROUP]);
    renderTab(`/logs/rejected?rejected=${GROUP.id}`);

    await waitFor(() =>
      expect(screen.getAllByText(GROUP.node_path).length).toBeGreaterThan(1),
    );
    expect(apiGet).toHaveBeenCalledWith(`/api/rejected/${GROUP.id}`);
  });

  it("карточка открывается и когда группы нет в выдаче списка (чужой период)", async () => {
    mockServer([]);
    renderTab(`/logs/rejected?rejected=${GROUP.id}`);

    await waitFor(() => expect(screen.getByText(GROUP.node_path)).toBeInTheDocument());
  });

  it("клик по строке пишет id в адрес", async () => {
    mockServer([GROUP]);
    renderTab();

    await waitFor(() => expect(screen.getByText(GROUP.node_path)).toBeInTheDocument());
    fireEvent.click(screen.getByText(GROUP.node_path));

    await waitFor(() =>
      expect(screen.getByTestId("loc").textContent).toContain(`rejected=${GROUP.id}`),
    );
  });

  it("«Поделиться» кладёт в буфер ссылку с id отказа и командой", async () => {
    mockServer([GROUP]);
    renderTab(`/logs/rejected?rejected=${GROUP.id}`);

    await waitFor(() => expect(screen.getByTitle("node.actions.share")).toBeInTheDocument());
    fireEvent.click(screen.getByTitle("node.actions.share"));

    await waitFor(() => expect(copyMock).toHaveBeenCalledTimes(1));
    const url = new URL(copyMock.mock.calls[0][0]);
    expect(url.pathname).toBe("/logs/rejected");
    expect(url.searchParams.get("rejected")).toBe(GROUP.id);
    // Без слога команды получатель откроет ссылку в СВОЕЙ команде и увидит
    // «не найдено»: карточка читается team-scoped эндпоинтом.
    expect(url.searchParams.get("team")).toBe(GROUP.team_slug);
  });
});
