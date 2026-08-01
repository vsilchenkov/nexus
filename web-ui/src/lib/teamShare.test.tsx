import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useEffect, useRef, type ReactNode } from "react";
import {
  MemoryRouter,
  useLocation,
  useNavigate,
  useNavigationType,
  useSearchParams,
  type NavigateFunction,
} from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { teamPageUrl, useTeamUrlParam } from "./teamShare";
import { MY_TEAMS_KEY, useMyTeams, type MyTeamsResp, type TeamMembership } from "./teams";

// §76: параметр `?team=<slug>` — зеркало текущей команды сессии в адресной
// строке плюс одноразовое применение входящей ссылки. Тесты фиксируют оба
// свойства и границы: маршруты узла (§58) и Настроек (§7.14.1) параметром не
// владеют, чужие query-ключи (фильтры §54) переживают запись, а уже разобранное
// значение сессию назад не откатывает.

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
  role: "admin",
};
const BETA: TeamMembership = {
  id: "team-b",
  slug: "beta",
  name: "Beta",
  ch_database: "nexus_beta",
  role: "admin",
};

// server — изменяемое состояние «бэкенда»: членства пользователя и текущая
// команда сессии. POST /api/me/switch-team меняет вторую, как настоящий
// эндпоинт; switchForbidden моделирует снятое членство (403).
type Server = {
  items: TeamMembership[];
  currentTeamID: string;
  switchForbidden?: boolean;
  teamsPending?: boolean;
};

function mockServer(s: Server) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/teams") {
      if (s.teamsPending) return new Promise<never>(() => {});
      const resp: MyTeamsResp = {
        items: s.items,
        current_team_id: s.currentTeamID,
        favorites: [],
      };
      return Promise.resolve(resp);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  apiPost.mockImplementation((url: string, body: { team_id: string }) => {
    if (url === "/api/me/switch-team") {
      if (s.switchForbidden) return Promise.reject({ response: { status: 403 } });
      s.currentTeamID = body.team_id;
      return Promise.resolve({});
    }
    return Promise.reject(new Error(`unexpected POST ${url}`));
  });
}

// staleTime как в проде (lib/queryClient.ts).
function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  });
}

// probe — наблюдаемое состояние роутера: адресная строка, тип последней
// навигации (REPLACE обязателен — иначе переключение команды ломает «Назад»)
// и navigate для эмуляции возврата на прежнюю ссылку.
const probe = { search: "", navType: "", navigate: null as NavigateFunction | null };

function RouterProbe() {
  probe.search = useLocation().search;
  probe.navType = useNavigationType();
  probe.navigate = useNavigate();
  return null;
}

function wrapperFor(qc: QueryClient, entry: string, rival = false) {
  return ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        {children}
        {rival && <RivalWriter />}
        <RouterProbe />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

// RivalWriter — модель рабочего стола (§71): на смену команды переписывает
// query-строку, сбрасывая период. Пишет ВТОРЫМ (объявлен после хука) и, как
// настоящий setSearchParams, видит в `prev` снимок своего рендера, а не
// актуальный URL — на этом и ломалось зеркало, пока запись шла в один проход.
//
// Важная деталь модели: период в тесте УЖЕ дефолтный, поэтому запись соседа
// ничего не меняет и итоговая строка совпадает с исходной. Именно так дефект и
// выглядел на стенде: location не меняется → рендера нет → починить некому.
// Если бы сосед реально что-то удалял, строка бы изменилась, рендер бы случился
// и зеркало исправилось само — тест был бы зелёным и на сломанном коде.
function RivalWriter() {
  const [, setParams] = useSearchParams();
  const { data } = useMyTeams();
  const teamID = data?.current_team_id ?? "";
  const seen = useRef("");
  useEffect(() => {
    if (!teamID || seen.current === teamID) return;
    const first = seen.current === "";
    seen.current = teamID;
    if (first) return; // первое появление команды сбросом не считается (§71.5)
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("range");
        return next;
      },
      { replace: true },
    );
  }, [teamID, setParams]);
  return null;
}

function render(server: Server, entry: string, rival = false) {
  mockServer(server);
  const qc = newClient();
  const hook = renderHook(() => useTeamUrlParam(), { wrapper: wrapperFor(qc, entry, rival) });
  return { qc, ...hook };
}

describe("useTeamUrlParam", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
    probe.search = "";
    probe.navType = "";
    probe.navigate = null;
  });

  it("применяет ссылку на другую команду ровно один раз и не затирает параметр", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/?team=beta");

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/me/switch-team", { team_id: "team-b" }),
    );
    await waitFor(() => expect(server.currentTeamID).toBe("team-b"));
    // Пока переезд в полёте, зеркало заморожено: параметр обязан остаться beta,
    // иначе ссылка затирает сама себя прежней командой.
    expect(probe.search).toBe("?team=beta");
    expect(apiPost).toHaveBeenCalledTimes(1);
  });

  it("параметр совпадает с текущей командой → ничего не делает", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/?team=alpha");

    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(apiPost).not.toHaveBeenCalled();
    expect(probe.navType).toBe("POP"); // записи в историю не было
  });

  it("параметра нет → дописывает текущую команду через replace", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/");

    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(probe.navType).toBe("REPLACE");
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("неизвестный slug → баннер и параметр приводится к текущей команде", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { result } = render(server, "/?team=zeta");

    await waitFor(() => expect(result.current.unavailableSlug).toBe("zeta"));
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("команда существует, но пользователь не её член → тот же путь (no-leak)", async () => {
    // Клиент видит только свои членства, поэтому «нет такой команды» и «я не
    // член» неотличимы — ровно как единый 404 резолвера узла в §58.
    const server: Server = { items: [ALPHA], currentTeamID: "team-a" };
    const { result } = render(server, "/?team=beta");

    await waitFor(() => expect(result.current.unavailableSlug).toBe("beta"));
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("403 на switch-team → баннер, зеркало размораживается", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a", switchForbidden: true };
    const { result } = render(server, "/?team=beta");

    await waitFor(() => expect(result.current.unavailableSlug).toBe("beta"));
    // Зеркало не залипло на недостижимой команде.
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
  });

  it("баннер закрывается вручную", async () => {
    const server: Server = { items: [ALPHA], currentTeamID: "team-a" };
    const { result } = render(server, "/?team=zeta");

    await waitFor(() => expect(result.current.unavailableSlug).toBe("zeta"));
    act(() => result.current.dismiss());
    expect(result.current.unavailableSlug).toBeNull();
  });

  it("страница узла: параметром владеет §58, мы не трогаем ни URL, ни сессию", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/nodes/n1?team=beta");

    await waitFor(() => expect(apiGet).toHaveBeenCalledWith("/api/me/teams"));
    await act(async () => {
      await Promise.resolve();
    });
    expect(apiPost).not.toHaveBeenCalled();
    expect(probe.search).toBe("?team=beta"); // как было, без нашей записи
  });

  it("Настройки вне скоупа команды: параметр не дописывается", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/settings/users");

    await waitFor(() => expect(apiGet).toHaveBeenCalledWith("/api/me/teams"));
    await act(async () => {
      await Promise.resolve();
    });
    expect(probe.search).toBe("");
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("ручное переключение команды в шапке обновляет параметр", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { qc } = render(server, "/");
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));

    act(() => {
      server.currentTeamID = "team-b";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-b" } : prev,
      );
    });

    await waitFor(() => expect(probe.search).toBe("?team=beta"));
    expect(apiPost).not.toHaveBeenCalled(); // переключил сам switcher, не мы
  });

  it("после применения ссылки ручное переключение не откатывается обратно", async () => {
    // Драка «ссылка против пользователя»: ссылка привела в Beta, человек тут же
    // ушёл в Alpha руками. Зеркало обязано записать Alpha и на этом успокоиться —
    // повторного switch-team на Beta быть не должно.
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { qc } = render(server, "/?team=beta");
    await waitFor(() => expect(server.currentTeamID).toBe("team-b"));

    act(() => {
      server.currentTeamID = "team-a";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-a" } : prev,
      );
    });

    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(apiPost).toHaveBeenCalledTimes(1); // только применение ссылки, отката нет
    expect(server.currentTeamID).toBe("team-a");
  });

  it("повторный переход по той же ссылке применяет её заново", async () => {
    // Одноразовость guard'а — про одно срабатывание на значение параметра, а не
    // «навсегда». Явный переход по ссылке (клик в мессенджере, ввод адреса) —
    // осознанное действие пользователя и обязан переключить команду снова.
    // Записи истории с прежним значением при этом не остаётся: зеркало пишет
    // параметр через replace.
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { qc } = render(server, "/?team=beta");
    await waitFor(() => expect(server.currentTeamID).toBe("team-b"));

    act(() => {
      server.currentTeamID = "team-a";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-a" } : prev,
      );
    });
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));

    act(() => probe.navigate?.("/?team=beta"));

    await waitFor(() => expect(server.currentTeamID).toBe("team-b"));
    expect(apiPost).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(probe.search).toBe("?team=beta"));
  });

  it("чужие query-параметры (фильтры §54) переживают запись", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/?q=foo&team=beta");

    await waitFor(() => expect(server.currentTeamID).toBe("team-b"));
    const params = new URLSearchParams(probe.search);
    expect(params.get("q")).toBe("foo");
    expect(params.get("team")).toBe("beta");
  });

  it("регистр в параметре нормализуется, пустое значение игнорируется", async () => {
    const upper: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(upper, "/?team=ALPHA");
    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
    expect(apiPost).not.toHaveBeenCalled();

    apiGet.mockReset();
    apiPost.mockReset();
    const empty: Server = { items: [ALPHA, BETA], currentTeamID: "team-b" };
    const { result } = render(empty, "/?team=");
    await waitFor(() => expect(probe.search).toBe("?team=beta"));
    expect(result.current.unavailableSlug).toBeNull(); // баннера с пустым именем быть не должно
  });

  it("второй писатель URL не откатывает параметр (гонка §71, нашёл стенд)", async () => {
    // Регресс на дефект, который не увидел ни один юнит-тест и поймал только
    // живой стенд: пользователь переключает команду в шапке, рабочий стол в том
    // же коммите переписывает query (сброс периода §71) своим устаревшим
    // снимком — и возвращает прежний `?team=`. Итоговая строка совпадала с
    // исходной, location не менялся, повторного рендера не было, и ссылка молча
    // оставалась врать.
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { qc } = render(server, "/?team=alpha", true);
    await waitFor(() => expect(qc.getQueryData(MY_TEAMS_KEY)).toBeTruthy());

    act(() => {
      server.currentTeamID = "team-b";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-b" } : prev,
      );
    });

    await waitFor(() => expect(new URLSearchParams(probe.search).get("team")).toBe("beta"));
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("сброс периода соседом не отменяется зеркалом", async () => {
    // Обратная сторона той же гонки: догоняя команду, мы не должны воскресить
    // ключи, которые сосед только что убрал (§71 сбрасывает период при смене
    // команды) — иначе лечение одного дефекта вводило бы другой.
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { qc } = render(server, "/?team=alpha&range=1h", true);
    await waitFor(() => expect(qc.getQueryData(MY_TEAMS_KEY)).toBeTruthy());

    act(() => {
      server.currentTeamID = "team-b";
      qc.setQueryData<MyTeamsResp>(MY_TEAMS_KEY, (prev) =>
        prev ? { ...prev, current_team_id: "team-b" } : prev,
      );
    });

    await waitFor(() => expect(new URLSearchParams(probe.search).get("team")).toBe("beta"));
    expect(new URLSearchParams(probe.search).has("range")).toBe(false);
  });

  it("завершающий слэш в адресе не отменяет параметр", async () => {
    // Адрес правят руками, а «/audit/» — тот же экран, что «/audit»: остаться
    // без ссылки он не должен.
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    render(server, "/audit/");

    await waitFor(() => expect(probe.search).toBe("?team=alpha"));
  });

  it("километровое значение параметра обрезается (баннер не распирает страницу)", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a" };
    const { result } = render(server, `/?team=${"z".repeat(500)}`);

    await waitFor(() => expect(result.current.unavailableSlug).not.toBeNull());
    expect(result.current.unavailableSlug).toHaveLength(64);
    expect(apiPost).not.toHaveBeenCalled();
  });

  it("членства ещё грузятся → ни записи в URL, ни переключения", async () => {
    const server: Server = { items: [ALPHA, BETA], currentTeamID: "team-a", teamsPending: true };
    render(server, "/?team=beta");

    await act(async () => {
      await Promise.resolve();
    });
    expect(probe.search).toBe("?team=beta"); // без мигания пустым team=
    expect(apiPost).not.toHaveBeenCalled();
  });
});

describe("teamPageUrl", () => {
  it("ведёт на рабочий стол с параметром команды", () => {
    expect(teamPageUrl("alpha")).toBe(`${window.location.origin}/?team=alpha`);
  });
});
