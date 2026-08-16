import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { SidebarFavorites } from "./SidebarFavorites";
import { PREF_KEY_FAVORITE_ALL_TEAMS } from "../lib/prefs";
import { setTeamScopeAll } from "../lib/teamScope";
import { type TeamMembership } from "../lib/teams";

// §89.8 — регресс, найденный ЖИВЫМ прогоном на стенде.
//
// После drop браузер шлёт click по перетащенному элементу. Он гасится флагом
// «только что был драг», но прежде флаг снимался макротаском (`setTimeout(0)`)
// в расчёте на то, что click прилетит раньше таймера. На стенде порядок
// оказался обратным: dnd-kit отдаёт onDragEnd не в том же тике, что pointerup,
// таймер срабатывал первым, и перетаскивание избранного заодно переключало
// команду (POST /api/me/switch-team), а перезагрузка списка откатывала только
// что сохранённый порядок.
//
// Теперь флаг снимает СЛЕДУЮЩЕЕ взаимодействие (pointerdown/keydown в фазе
// захвата) — порядок детерминирован. Тест проверяет обе стороны контракта:
// клик сразу после драга гасится, а клик после нового pointerdown проходит.
//
// Отдельный файл, потому что нужен доступ к колбэкам DndContext: в jsdom нет
// PointerEvent, сенсор dnd-kit не запускается, и драг иначе не смоделировать.

const dnd = vi.hoisted(() => ({
  handlers: {} as { onDragStart?: () => void; onDragEnd?: (e: unknown) => void },
}));

vi.mock("@dnd-kit/core", async () => {
  const actual = await vi.importActual<typeof import("@dnd-kit/core")>("@dnd-kit/core");
  return {
    ...actual,
    DndContext: (props: { children: ReactNode; onDragStart?: () => void; onDragEnd?: (e: unknown) => void }) => {
      dnd.handlers.onDragStart = props.onDragStart;
      dnd.handlers.onDragEnd = props.onDragEnd;
      return props.children;
    },
  };
});

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

beforeEach(() => {
  apiGet.mockReset();
  apiPost.mockReset();
  apiPost.mockResolvedValue({});
  sessionStorage.clear();
  setTeamScopeAll(false);
  dnd.handlers = {};
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
        items: [{ team_id: "", key: PREF_KEY_FAVORITE_ALL_TEAMS, value: true, updated_at: "" }],
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
});

async function renderAndDrag() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/audit?team=alpha"]}>
        <SidebarFavorites />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  const link = await screen.findByRole("link", { name: "Beta" });

  // Настоящий драг: pointerdown (новое взаимодействие) → onDragStart → drop.
  fireEvent.pointerDown(link, { button: 0 });
  act(() => dnd.handlers.onDragStart?.());
  act(() => dnd.handlers.onDragEnd?.({ active: { id: BETA.id }, over: { id: BETA.id } }));

  // Даём отработать таймерам и микрозадачам: прежний сброс успевал здесь, и
  // именно поэтому клик проходил как обычный.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
  return link;
}

describe("SidebarFavorites — клик после драга", () => {
  it("не переключает команду", async () => {
    const link = await renderAndDrag();

    link.click();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(apiPost).not.toHaveBeenCalled();
  });

  it("следующий клик (после нового pointerdown) снова работает", async () => {
    const link = await renderAndDrag();

    link.click(); // этот гасится
    fireEvent.pointerDown(link, { button: 0 }); // новое взаимодействие
    link.click();
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: BETA.id });
    expect(apiPost).toHaveBeenCalledTimes(1);
  });
});
