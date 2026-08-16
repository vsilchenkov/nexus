import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ConfigTab } from "./ConfigTab";
import { type Node } from "../../api/client";

// §89.4: блок «Полный адрес» получает третью строку — внешний адрес узла
// (внешняя ссылка команды + путь узла). §89.6: команда берётся ОТ УЗЛА, а не из
// сессии — узел может принадлежать команде, которой нет в членствах смотрящего.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const NODE = {
  id: "node-1",
  team_id: "team-b",
  path: "ozon",
  root_method: "requestAsync",
  incoming_method: "POST",
  outgoing_method: "POST",
  url_mode: "static",
  target_url: "https://api.ozon.ru/hook",
  status: "active",
} as unknown as Node;

// Команда УЗЛА (team-b) в членствах отсутствует — есть только team-a. Данные о
// ней приходят исключительно из резолвера §58.
function mockServer(nodeTeamExternal: string) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/settings/public") {
      return Promise.resolve({ public_base_url: "https://nexus.example.com" });
    }
    if (url === "/api/me/teams") {
      return Promise.resolve({
        items: [
          {
            id: "team-a",
            slug: "alpha",
            name: "Alpha",
            ch_database: "nexus_alpha",
            external_url: "https://WRONG.example.com",
            role: "admin",
          },
        ],
        current_team_id: "team-a",
        favorites: [],
      });
    }
    if (url === "/api/nodes/node-1/team") {
      return Promise.resolve({
        team_id: "team-b",
        team_slug: "beta",
        team_name: "Beta",
        team_external_url: nodeTeamExternal,
      });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderConfig() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      {/* useMyTeams смотрит на location (выключен на /login). */}
      <MemoryRouter initialEntries={["/nodes/node-1"]}>
        <ConfigTab node={NODE} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
});

describe("ConfigTab — блок «Полный адрес»", () => {
  it("показывает внешний адрес: ссылка команды узла + путь узла", async () => {
    mockServer("https://gw.partner.ru/nexus");
    renderConfig();

    await waitFor(() =>
      expect(screen.getByText(/https:\/\/gw\.partner\.ru\/nexus\/ozon/)).toBeInTheDocument(),
    );
  });

  it("у команды без внешней ссылки третьей строки нет", async () => {
    mockServer("");
    renderConfig();

    // Дожидаемся короткого адреса, иначе «нет строки» подтвердилось бы просто
    // потому, что компонент ещё не догрузился.
    await waitFor(() =>
      expect(screen.getByText("https://nexus.example.com/api/v1/beta/ozon")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/node\.form\.external_address/)).toBeNull();
  });

  // §89.6: до фикса адрес собирался по slug'у ТЕКУЩЕЙ команды сессии. Здесь
  // узел принадлежит beta, а сессия сидит в alpha — и адрес обязан быть beta.
  it("адрес собран по команде узла, а не по команде сессии", async () => {
    mockServer("https://gw.partner.ru");
    renderConfig();

    await waitFor(() =>
      expect(screen.getByText("https://nexus.example.com/api/v1/beta/ozon")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/api\/v1\/alpha\//)).toBeNull();
    expect(screen.queryByText(/WRONG\.example\.com/)).toBeNull();
  });
});
