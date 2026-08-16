import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useEffect, useRef, type ReactNode } from "react";
import { MemoryRouter, useLocation, useSearchParams } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  setTeamScopeAll,
  teamScopeKey,
  scopeParams,
  useAllTeamsScope,
  TEAM_SCOPE_ALL,
} from "./teamScope";
import { useTeamUrlParam } from "./teamShare";
import { type MyTeamsResp, type TeamMembership } from "./teams";

// §86: сквозной режим «Все команды» и его стыковка с зеркалом §76.
//
// Стыковка — самое хрупкое место раздела: `useTeamUrlParam` держит единственного
// писателя параметра с двумя ref-guard'ами и двухпроходной записью против гонки
// (§76.7), а `*` обязан выпасть из этой машины целиком. Прошлый дефект зеркала
// нашёл ТОЛЬКО живой стенд, поэтому границы закреплены тестами.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost } };
});

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

function mockServer(currentTeamID = "team-a") {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      const resp: MyTeamsResp = {
        items: [ALPHA, BETA],
        current_team_id: currentTeamID,
        favorites: [],
      };
      return Promise.resolve(resp);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  apiPost.mockImplementation((url: string) => Promise.reject(new Error(`unexpected POST ${url}`)));
}

const probe = { search: "", allTeams: false };

function RouterProbe() {
  probe.search = useLocation().search;
  // Режим читаем пробой, а не по адресной строке: во входящей ссылке `*` стоит
  // в URL С САМОГО НАЧАЛА, и ожидание «в адресе есть team=*» выполняется ещё до
  // того, как режим включится. На этом артефакте тест «выхода из режима»
  // сначала показал ложный отказ.
  probe.allTeams = useAllTeamsScope();
  return null;
}

// RivalWriter — рабочий стол §71, пишущий query-строку вторым (см. подробный
// разбор гонки в teamShare.test.tsx). Здесь он нужен, чтобы убедиться: режим
// переживает чужую перезапись параметров.
function RivalWriter() {
  const [, setParams] = useSearchParams();
  const fired = useRef(false);
  useEffect(() => {
    if (fired.current) return;
    fired.current = true;
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.set("range", "7d");
        return next;
      },
      { replace: true },
    );
  }, [setParams]);
  return null;
}

function render(entry: string, rival = false) {
  mockServer();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        {children}
        {rival && <RivalWriter />}
        <RouterProbe />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return renderHook(() => useTeamUrlParam(), { wrapper });
}

describe("сквозной режим «Все команды» (§86)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
    probe.search = "";
    probe.allTeams = false;
    setTeamScopeAll(false);
    sessionStorage.clear();
  });

  it("ссылка /?team=* включает режим, не переключая команду и не поднимая баннер", async () => {
    const { result } = render(`/?team=${encodeURIComponent(TEAM_SCOPE_ALL)}`);

    await waitFor(() => expect(probe.allTeams).toBe(true));
    expect(probe.search).toContain("team=*");
    // Ключевое: `*` разбирается ДО резолва slug'а. Иначе он не нашёлся бы среди
    // членств, поднял бы баннер «команда недоступна», а параметр тут же
    // переписался бы на текущую команду — режим не включился бы ни разу.
    expect(result.current.unavailableSlug).toBeNull();
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("зеркало не подменяет `*` слогом текущей команды", async () => {
    render(`/?team=${encodeURIComponent(TEAM_SCOPE_ALL)}`);

    await waitFor(() => expect(probe.allTeams).toBe(true));
    // Даём зеркалу отработать несколько раз: инвариант §76.4 («параметр = slug
    // текущей команды») для режима не действует, и залипания быть не должно.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(probe.search).toContain("team=*");
    expect(probe.search).not.toContain("team=alpha");
  });

  it("режим переживает перезапись query-строки соседним писателем (§54/§71)", async () => {
    render(`/?team=${encodeURIComponent(TEAM_SCOPE_ALL)}`, true);

    await waitFor(() => expect(probe.search).toContain("range=7d"));
    expect(probe.search).toContain("team=*");
  });

  it("включённый режим возвращает параметр на чистый адрес", async () => {
    // Сценарий кнопки «Узлы» в сайдбаре: она открывает "/" вообще без query.
    setTeamScopeAll(true);
    render("/");

    await waitFor(() => expect(probe.search).toContain("team=*"));
  });

  it("выключённый режим приводит параметр к текущей команде", async () => {
    render("/");

    await waitFor(() => expect(probe.search).toContain("team=alpha"));
    expect(probe.search).not.toContain("team=*");
  });

  it("выход из режима приводит параметр к команде, а не включает режим обратно", async () => {
    // Сценарий: оператор в сквозном режиме выбирает конкретную команду.
    // `setTeamScopeAll(false)` будит тот же эффект, а в URL всё ещё `*` — без
    // одноразовости применения режим включался бы обратно, и переключение
    // выглядело бы как «кнопка не реагирует».
    render(`/?team=${encodeURIComponent(TEAM_SCOPE_ALL)}`);
    await waitFor(() => expect(probe.allTeams).toBe(true));

    act(() => setTeamScopeAll(false));

    await waitFor(() => expect(probe.search).toContain("team=alpha"));
    expect(probe.search).not.toContain("team=*");
  });

  it("на маршрутах без параметра (§76.3) режим ничего не пишет", async () => {
    setTeamScopeAll(true);
    render("/nodes/n1");

    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(probe.search).toBe("");
  });
});

describe("ключи и параметры скоупа (§86.3)", () => {
  beforeEach(() => setTeamScopeAll(false));

  it("ключ кеша различает режимы — иначе выдачи алиасятся в один слот", () => {
    // Правило §4.44: без скоупа в ключе react-query мгновенно отдаёт список
    // прежнего режима (stale-while-revalidate).
    expect(teamScopeKey(true, "team-a")).not.toBe(teamScopeKey(false, "team-a"));
    expect(teamScopeKey(false, "team-a")).toBe("team-a");
    expect(teamScopeKey(true, "team-a")).toBe(TEAM_SCOPE_ALL);
  });

  it("scope=all уходит в запрос только в сквозном режиме", () => {
    expect(scopeParams(true)).toEqual({ scope: "all" });
    expect(scopeParams(false)).toEqual({});
  });

  it("режим переживает перезагрузку страницы через sessionStorage", () => {
    setTeamScopeAll(true);
    expect(sessionStorage.getItem("nexus.team.scope")).toBe("1");
    setTeamScopeAll(false);
    expect(sessionStorage.getItem("nexus.team.scope")).toBeNull();
  });
});
