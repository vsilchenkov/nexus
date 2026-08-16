import { TooltipProvider } from "@radix-ui/react-tooltip";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { TeamSwitcher } from "./TeamSwitcher";
import { setTeamScopeAll } from "../lib/teamScope";
import { type TeamMembership } from "../lib/teams";

// §89.3: строки переключателя команд в шапке — такие же настоящие ссылки, как
// строки «Избранного». Проверяется ПРИРОДА элемента, а не поведение клика: с
// <button> контекстное меню «Открыть в новой вкладке» и Ctrl+клик не работают
// вовсе, и никакая проверка клика этого не замечает (урок §79.3).

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
  role: "admin",
};
const BETA: TeamMembership = {
  id: "team-b",
  slug: "beta",
  name: "Beta",
  ch_database: "nexus_beta",
  role: "admin",
};

beforeEach(() => {
  apiGet.mockReset();
  apiPost.mockReset();
  sessionStorage.clear();
  setTeamScopeAll(false);
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      return Promise.resolve({ items: [ALPHA, BETA], current_team_id: ALPHA.id, favorites: [] });
    }
    if (url === "/api/me/prefs") return Promise.resolve({ items: [] });
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
});

async function openSwitcher() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/"]}>
        {/* В приложении провайдер монтируется один раз в AppShell (§25). */}
        <TooltipProvider>
          <TeamSwitcher />
        </TooltipProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  const trigger = await screen.findByLabelText("teams.switcher_label");
  trigger.click();
  await screen.findByRole("link", { name: "Beta" });
}

describe("TeamSwitcher", () => {
  it("строка команды — ссылка на её рабочий стол", async () => {
    await openSwitcher();

    const link = screen.getByRole("link", { name: "Beta" });
    expect(link).toHaveAttribute("href", "/?team=beta");
    expect(screen.queryByRole("button", { name: "Beta" })).toBeNull();
  });

  it("строка сквозного режима — ссылка на ?team=*", async () => {
    await openSwitcher();

    expect(screen.getByRole("link", { name: "teams.all" })).toHaveAttribute("href", "/?team=*");
  });

  it("обычный левый клик переключает команду сессии", async () => {
    apiPost.mockResolvedValue({});
    await openSwitcher();

    screen.getByRole("link", { name: "Beta" }).click();

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: BETA.id }),
    );
  });

  it("Ctrl+клик отдаётся браузеру и не трогает текущую вкладку", async () => {
    apiPost.mockResolvedValue({});
    await openSwitcher();

    screen
      .getByRole("link", { name: "Beta" })
      .dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true, button: 0 }),
      );

    await Promise.resolve();
    expect(apiPost).not.toHaveBeenCalled();
  });

  // §49.2: звезда и «Поделиться» — СОСЕДИ ссылки, а не потомки. Кнопка внутри
  // <a> — невалидная разметка, и клик по звезде уводил бы на рабочий стол.
  it("звезда и «Поделиться» не вложены в ссылку", async () => {
    await openSwitcher();

    const link = screen.getByRole("link", { name: "Beta" });
    expect(link.querySelector("button")).toBeNull();
    // По кнопке «Поделиться» на каждую команду; у строки сквозного режима её
    // нет (§86: у режима нет slug'а).
    expect(screen.getAllByLabelText("teams.share")).toHaveLength(2);
  });
});
