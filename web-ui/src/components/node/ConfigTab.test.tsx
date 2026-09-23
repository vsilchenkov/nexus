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

// §99.7: строка «Группа» на вкладке «Конфиг» — сразу после «Команды».
describe("ConfigTab — строка «Группа»", () => {
  function mockWithGroups(groups: Array<{ id: string; name: string }>) {
    apiGet.mockImplementation((url: string) => {
      if (url === "/api/settings/public") {
        return Promise.resolve({ public_base_url: "https://nexus.example.com" });
      }
      if (url === "/api/me/teams") {
        return Promise.resolve({ items: [], current_team_id: "team-a", favorites: [] });
      }
      if (url === "/api/nodes/node-1/team") {
        return Promise.resolve({
          team_id: "team-b",
          team_slug: "beta",
          team_name: "Beta",
          team_external_url: "",
        });
      }
      if (url === "/api/node-groups") {
        return Promise.resolve({
          items: groups.map((g) => ({
            ...g,
            description: "",
            sort_order: 10,
            usage_count: 1,
            created_by: "",
            updated_by: "",
            created_at: "2026-09-01T00:00:00Z",
            updated_at: "2026-09-01T00:00:00Z",
          })),
        });
      }
      return Promise.reject(new Error(`unexpected GET ${url}`));
    });
  }

  function renderWithGroup(groupID?: string) {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/nodes/node-1"]}>
          <ConfigTab node={{ ...NODE, group_id: groupID } as Node} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  it("показывает имя группы узла", async () => {
    mockWithGroups([{ id: "g1", name: "1С Обмен" }]);
    renderWithGroup("g1");
    await waitFor(() => expect(screen.getByText("1С Обмен")).toBeInTheDocument());
  });

  it("у узла без группы строка показывает прочерк и справочник не запрашивается", async () => {
    mockWithGroups([{ id: "g1", name: "1С Обмен" }]);
    renderWithGroup(undefined);
    // Дожидаемся отрисовки конфига, иначе «прочерк» подтвердился бы просто
    // потому, что компонент ещё не догрузился.
    await waitFor(() => expect(screen.getByText("node.fields.group")).toBeInTheDocument());
    expect(screen.queryByText("1С Обмен")).toBeNull();
    // Лишний запрос справочника на каждый узел без группы — плата ни за что.
    expect(apiGet.mock.calls.some((c) => c[0] === "/api/node-groups")).toBe(false);
  });

  it("группа удалена из справочника — строка не пустая, а с прочерком", async () => {
    mockWithGroups([]);
    renderWithGroup("g-gone");
    await waitFor(() => expect(screen.getByText("node.fields.group")).toBeInTheDocument());
    // Узел важнее своей метки (§99.9): экран не падает и не прячет строку.
    expect(screen.queryByText("1С Обмен")).toBeNull();
  });
});

// §107: строки флагов потока SSE и таймаутов — только при включённом флаге.
// Выключенный флаг означает прежнее поведение, строка «нет» была бы шумом.
describe("ConfigTab — флаги §107", () => {
  function renderFlags(patch: Partial<Node>) {
    mockServer("");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/nodes/node-1"]}>
          <ConfigTab node={{ ...NODE, ...patch } as Node} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }

  it("sync-узел с обоими флагами показывает обе строки", async () => {
    renderFlags({ root_method: "request", sse_decline: true, breaker_ignore_timeouts: true });
    await waitFor(() => expect(screen.getByText("node.fields.sse_decline")).toBeInTheDocument());
    expect(screen.getByText("node.fields.sse_decline_value")).toBeInTheDocument();
    expect(screen.getByText("node.fields.breaker_ignore_timeouts_value")).toBeInTheDocument();
  });

  it("выключенные флаги строк не дают", async () => {
    renderFlags({ root_method: "request", sse_decline: false, breaker_ignore_timeouts: false });
    await waitFor(() => expect(screen.getByText("node.form.incoming_method")).toBeInTheDocument());
    expect(screen.queryByText("node.fields.sse_decline")).toBeNull();
    expect(screen.queryByText("node.fields.breaker_ignore_timeouts")).toBeNull();
  });

  it("у async-узла строки SSE нет даже при устаревшем флаге, строка таймаутов есть", async () => {
    renderFlags({ root_method: "requestAsync", sse_decline: true, breaker_ignore_timeouts: true });
    await waitFor(() =>
      expect(screen.getByText("node.fields.breaker_ignore_timeouts")).toBeInTheDocument(),
    );
    expect(screen.queryByText("node.fields.sse_decline")).toBeNull();
  });
});
