import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SidebarFavorites } from "./SidebarFavorites";
import { PREF_KEY_FAVORITE_ALL_TEAMS } from "../lib/prefs";
import { setTeamScopeAll } from "../lib/teamScope";
import { type TeamMembership } from "../lib/teams";

// §89.3: строки «Избранного» обязаны быть НАСТОЯЩИМИ ссылками. С <button>
// адрес рабочего стола команды существовал (§76.2), но открывать его браузеру
// было нечем: контекстное меню «Открыть в новой вкладке», Ctrl/Cmd+клик и клик
// средней кнопкой не работали вовсе. Тесты проверяют ПРИРОДУ элемента, а не
// поведение клика — урок §79.3: 329 фронт-тестов пропустили ровно этот дефект,
// потому что проверяли клик.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const ALPHA: TeamMembership = {
  id: "team-a",
  slug: "alpha",
  name: "Alpha",
  ch_database: "nexus_alpha",
  external_url: "",
  role: "admin",
};
const BETA: TeamMembership = {
  id: "team-b",
  slug: "beta",
  name: "Beta",
  ch_database: "nexus_beta",
  external_url: "",
  role: "admin",
};

function mockServer({ allFavorite = true }: { allFavorite?: boolean } = {}) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      return Promise.resolve({
        items: [ALPHA, BETA],
        current_team_id: ALPHA.id,
        favorites: [ALPHA.id, BETA.id],
      });
    }
    if (url === "/api/me/prefs") {
      return Promise.resolve({
        items: allFavorite
          ? [{ team_id: "", key: PREF_KEY_FAVORITE_ALL_TEAMS, value: true, updated_at: "" }]
          : [],
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderFavorites() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      {/* Не "/login": там useMyTeams выключен (enabled: false). */}
      <MemoryRouter initialEntries={["/audit?team=alpha"]}>
        <SidebarFavorites />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiPost.mockReset();
  sessionStorage.clear();
  setTeamScopeAll(false);
});

describe("SidebarFavorites", () => {
  it("команда — ссылка на её рабочий стол, а не кнопка", async () => {
    mockServer();
    renderFavorites();

    const link = await screen.findByRole("link", { name: "Beta" });
    expect(link).toHaveAttribute("href", "/?team=beta");
    // Прямой регресс на спред attributes из @dnd-kit: там role="button", и
    // явная роль на <a> перебила бы нативную — элемент перестал бы быть ссылкой
    // и для скринридера, и для этого теста.
    expect(link.getAttribute("role")).toBe("link");
    expect(screen.queryByRole("button", { name: "Beta" })).toBeNull();
  });

  it("ссылка не перехватывается нативным драгом браузера", async () => {
    mockServer();
    renderFavorites();

    // Без draggable={false} браузер начинает свой HTML5-драг ссылки, показывает
    // «призрак» адреса, и сортировка @dnd-kit не стартует вовсе.
    const link = await screen.findByRole("link", { name: "Beta" });
    expect(link).toHaveAttribute("draggable", "false");
  });

  it("«Все команды» ведёт на сквозной режим, и `*` не экранируется", async () => {
    mockServer();
    renderFavorites();

    const link = await screen.findByRole("link", { name: "teams.all" });
    expect(link).toHaveAttribute("href", "/?team=*");
  });

  it("строки режима нет, когда он не в избранном", async () => {
    mockServer({ allFavorite: false });
    renderFavorites();

    await screen.findByRole("link", { name: "Beta" });
    expect(screen.queryByRole("link", { name: "teams.all" })).toBeNull();
  });

  it("обычный левый клик переключает команду сессии", async () => {
    mockServer();
    apiPost.mockResolvedValue({});
    const { container } = renderFavorites();

    const link = await screen.findByRole("link", { name: "Beta" });
    link.click();

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: BETA.id }),
    );
    expect(container).toBeTruthy();
  });

  // Ctrl+клик — просьба браузера открыть href в НОВОЙ вкладке. Побочка при этом
  // срабатывать не должна: она переключила бы команду в текущей вкладке, которую
  // пользователь трогать не просил.
  it("Ctrl+клик отдаётся браузеру и не трогает текущую вкладку", async () => {
    mockServer();
    apiPost.mockResolvedValue({});
    renderFavorites();

    const link = await screen.findByRole("link", { name: "Beta" });
    link.dispatchEvent(
      new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true, button: 0 }),
    );

    await Promise.resolve();
    expect(apiPost).not.toHaveBeenCalled();
  });
});
