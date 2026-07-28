import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useEnsureNodeTeam } from "./nodeShare";
import { MY_TEAMS_KEY, type MyTeamsResp } from "./teams";

// §58 + перенос узла между командами (Phase 11.B): авто-переключение сессии на
// команду открываемого узла обязано опираться на СВЕЖУЮ команду узла.
// Регрессия: после переноса узла в другую команду его открытие уводило сессию
// в СТАРУЮ команду — закешированный ответ GET /api/nodes/:id/team описывал
// команду до переноса, а одноразовый guard залипал на нём навсегда.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost } };
});

const TEAMS = [
  { id: "team-a", slug: "alpha", name: "Alpha", ch_database: "nexus_alpha", role: "admin" as const },
  { id: "team-b", slug: "beta", name: "Beta", ch_database: "nexus_beta", role: "admin" as const },
];

// server — изменяемое состояние «бэкенда»: команда узла и текущая команда
// сессии. POST /api/me/switch-team меняет вторую, как настоящий эндпоинт.
type Server = { nodeTeamID: string; currentTeamID: string; notFound?: boolean };

function mockServer(s: Server) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      const resp: MyTeamsResp = {
        items: TEAMS,
        current_team_id: s.currentTeamID,
        favorites: [],
      };
      return Promise.resolve(resp);
    }
    if (url === "/api/nodes/n1/team") {
      if (s.notFound) return Promise.reject({ response: { status: 404 } });
      const team = TEAMS.find((t) => t.id === s.nodeTeamID)!;
      return Promise.resolve({ team_id: team.id, team_slug: team.slug, team_name: team.name });
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  apiPost.mockImplementation((url: string, body: { team_id: string }) => {
    if (url === "/api/me/switch-team") {
      s.currentTeamID = body.team_id;
      return Promise.resolve({});
    }
    return Promise.reject(new Error(`unexpected POST ${url}`));
  });
}

// staleTime как в проде (lib/queryClient.ts): именно он делал закешированную
// команду узла «свежей» и не давал рефетчу исправить решение.
function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  });
}

function wrapperFor(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={["/nodes/n1"]}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
}

describe("useEnsureNodeTeam", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
  });

  it("не уводит сессию в старую команду по закешированному ответу (узел перенесён)", async () => {
    // Узел уехал из Alpha в Beta, сессия уже в Beta, но в кеше висит ответ
    // «узел в Alpha», снятый до переноса.
    const server: Server = { nodeTeamID: "team-b", currentTeamID: "team-b" };
    mockServer(server);
    const qc = newClient();
    qc.setQueryData(["node-team", "n1"], {
      team_id: "team-a",
      team_slug: "alpha",
      team_name: "Alpha",
    });

    const { result } = renderHook(() => useEnsureNodeTeam("n1"), { wrapper: wrapperFor(qc) });

    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(apiPost).not.toHaveBeenCalled();
    expect(server.currentTeamID).toBe("team-b");
  });

  it("переключает сессию на команду узла (шаренная ссылка, §58 п.2)", async () => {
    const server: Server = { nodeTeamID: "team-b", currentTeamID: "team-a" };
    mockServer(server);
    const qc = newClient();

    const { result } = renderHook(() => useEnsureNodeTeam("n1"), { wrapper: wrapperFor(qc) });

    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: "team-b" });
    expect(server.currentTeamID).toBe("team-b");
  });

  it("не откатывает ручное переключение команды в шапке", async () => {
    const server: Server = { nodeTeamID: "team-a", currentTeamID: "team-a" };
    mockServer(server);
    const qc = newClient();

    const { result } = renderHook(() => useEnsureNodeTeam("n1"), { wrapper: wrapperFor(qc) });
    await waitFor(() => expect(result.current.status).toBe("ready"));

    // Пользователь осознанно сменил команду в шапке — не возвращаем его назад.
    act(() => {
      server.currentTeamID = "team-b";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-b" } : prev,
      );
    });

    expect(apiPost).not.toHaveBeenCalled();
    expect(result.current.status).toBe("ready");
  });

  it("догоняет перенос узла, случившийся при открытой странице", async () => {
    const server: Server = { nodeTeamID: "team-a", currentTeamID: "team-a" };
    mockServer(server);
    const qc = newClient();

    const { result } = renderHook(() => useEnsureNodeTeam("n1"), { wrapper: wrapperFor(qc) });
    await waitFor(() => expect(result.current.status).toBe("ready"));

    // Узел перенесли (мы сами из другой вкладки или другой админ) — свежий
    // ответ приносит новую команду, сессия обязана переехать за узлом.
    server.nodeTeamID = "team-b";
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ["node-team", "n1"] });
    });

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: "team-b" }),
    );
    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(server.currentTeamID).toBe("team-b");
  });

  it("404 (узла нет или команда недоступна) → unavailable", async () => {
    mockServer({ nodeTeamID: "team-a", currentTeamID: "team-a", notFound: true });
    const qc = newClient();

    const { result } = renderHook(() => useEnsureNodeTeam("n1"), { wrapper: wrapperFor(qc) });

    await waitFor(() => expect(result.current.status).toBe("unavailable"));
    expect(apiPost).not.toHaveBeenCalled();
  });
});
