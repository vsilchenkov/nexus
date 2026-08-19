import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Overview from "./Overview";
import { ME_PREFS_KEY, PREF_KEY_OVERVIEW_PERIOD } from "../lib/prefs";
import { MY_TEAMS_KEY, type MyTeamsResp } from "../lib/teams";
import { setTeamScopeAll } from "../lib/teamScope";

// §71: дефолтный период рабочего стола — персональный и свой у каждой команды.
//
// Регресс, красный на прежнем коде: дефолт был ОДИН на браузер
// (localStorage nexus.overview.period), поэтому после смены команды на рабочем
// столе оставался период чужой команды.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, put: apiPut } };
});

// i18n в тестах не инициализирован — t() возвращает ключ; проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const TEAM_A = "team-a";
const TEAM_B = "team-b";

const TEAMS = [
  { id: TEAM_A, slug: "alpha", name: "Alpha", ch_database: "nexus_alpha", external_url: "", role: "admin" as const },
  { id: TEAM_B, slug: "beta", name: "Beta", ch_database: "nexus_beta", external_url: "", role: "admin" as const },
];

// server — изменяемое состояние «бэкенда»: текущая команда сессии и префы.
type Server = {
  currentTeamID: string;
  prefs: { team_id: string; range: string }[];
  prefsFail?: boolean;
};

// prefItem — запись в ответе GET /api/me/prefs.
function prefItem(teamID: string, range: string) {
  return {
    team_id: teamID,
    key: PREF_KEY_OVERVIEW_PERIOD,
    value: { kind: "preset", range },
    updated_at: "",
  };
}

// metricsCalls — параметры каждого запроса /api/metrics/nodes: по ним видно и
// сколько раз он ушёл, и с каким периодом (анти-мигание §71).
let metricsCalls: Record<string, unknown>[] = [];

function mockServer(s: Server) {
  metricsCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/me/teams") {
      const resp: MyTeamsResp = {
        items: TEAMS,
        current_team_id: s.currentTeamID,
        favorites: [],
      };
      return Promise.resolve(resp);
    }
    if (url === "/api/me/prefs") {
      if (s.prefsFail) return Promise.reject({ response: { status: 500 } });
      return Promise.resolve({
        items: s.prefs.map((p) => ({
          team_id: p.team_id,
          key: PREF_KEY_OVERVIEW_PERIOD,
          value: { kind: "preset", range: p.range },
          updated_at: "",
        })),
      });
    }
    if (url === "/api/nodes") return Promise.resolve({ items: [] });
    if (url === "/api/metrics/overview") {
      return Promise.resolve({
        incoming_24h: 0, outgoing_24h: 0, kafka_queue: 0,
        errors_24h: 0, error_rate: 0, prometheus_available: true,
      });
    }
    if (url === "/api/metrics/totals") {
      // §86.4: в сквозном режиме шапка считается этим запросом, а /metrics/nodes
      // не зовётся вовсе — период наблюдаем здесь.
      metricsCalls.push(params ?? {});
      return Promise.resolve({
        totals: { incoming: 0, outgoing: 0, errors: 0, error_rate: 0 },
        items: [],
        prometheus_available: true,
      });
    }
    if (url === "/api/metrics/nodes") {
      metricsCalls.push(params ?? {});
      return Promise.resolve({
        items: [],
        totals: { incoming: 0, outgoing: 0, errors: 0, error_rate: 0 },
        prometheus_available: true,
      });
    }
    if (url === "/api/me/search-history") return Promise.resolve({ items: [] });
    if (url === "/api/settings/public") return Promise.resolve({});
    if (url === "/api/auth/me") return Promise.resolve({ user: { role: "admin" } });
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
}

// urlProbe — наблюдаемая адресная строка MemoryRouter (window.location в тестах
// не используется). Нужна кейсу §76: правка фильтров не должна выбрасывать из
// URL чужие ключи, в том числе ссылку на команду.
const urlProbe = { search: "" };

function UrlProbe() {
  urlProbe.search = useLocation().search;
  return null;
}

function renderOverview(qc: QueryClient, entry = "/") {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>
        {children}
        <UrlProbe />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<Overview />, { wrapper });
}

// lastMetricsRange — период последнего запроса /api/metrics/nodes. Проверяем
// именно его, а не подсветку кнопки: Seg активность через ARIA не выражает, а
// «какой период показан» наблюдаемо ровно тем, за какой период грузятся данные.
function lastMetricsRange(): unknown {
  return metricsCalls.at(-1)?.range;
}

describe("Overview: дефолтный период per-team (§71)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
  });

  it("показывает дефолт текущей команды, а не системные 24ч", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "30d" }] });
    renderOverview(newClient());

    await waitFor(() => expect(lastMetricsRange()).toBe("30d"));
  });

  // Главный регресс: на прежнем коде дефолт был общий (один localStorage-ключ
  // на браузер), и после переключения команды период оставался прежним.
  it("после переключения команды показывает дефолт НОВОЙ команды", async () => {
    const server: Server = {
      currentTeamID: TEAM_A,
      prefs: [
        { team_id: TEAM_A, range: "30d" },
        { team_id: TEAM_B, range: "7d" },
      ],
    };
    mockServer(server);
    const qc = newClient();
    renderOverview(qc);

    await waitFor(() => expect(lastMetricsRange()).toBe("30d"));

    // Сессия переключилась на другую команду (топбар/§58) → членства перечитаны.
    server.currentTeamID = TEAM_B;
    await qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });

    await waitFor(() => expect(lastMetricsRange()).toBe("7d"));
  });

  // Переключение команды сбрасывает даже выбранный вручную период: работая в
  // двух командах с разными горизонтами, иначе приходится переставлять руками.
  it("переключение команды сбрасывает даже период, выбранный вручную", async () => {
    const server: Server = {
      currentTeamID: TEAM_A,
      prefs: [
        { team_id: TEAM_A, range: "30d" },
        { team_id: TEAM_B, range: "7d" },
      ],
    };
    mockServer(server);
    const qc = newClient();
    renderOverview(qc, "/?range=1h");

    await waitFor(() => expect(lastMetricsRange()).toBe("1h"));

    server.currentTeamID = TEAM_B;
    await qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });

    await waitFor(() => expect(lastMetricsRange()).toBe("7d"));
  });

  // §76: сброс периода при смене команды переписывает query-строку. Ссылка на
  // команду живёт в ней же, и вылететь при этом не должна — иначе адрес
  // страницы переставал бы быть ссылкой ровно в момент переключения.
  it("сброс периода при смене команды не выбрасывает из URL параметр ?team=", async () => {
    const server: Server = {
      currentTeamID: TEAM_A,
      prefs: [
        { team_id: TEAM_A, range: "30d" },
        { team_id: TEAM_B, range: "7d" },
      ],
    };
    mockServer(server);
    const qc = newClient();
    renderOverview(qc, "/?team=alpha&range=1h");

    await waitFor(() => expect(lastMetricsRange()).toBe("1h"));

    server.currentTeamID = TEAM_B;
    await qc.invalidateQueries({ queryKey: MY_TEAMS_KEY });

    await waitFor(() => expect(lastMetricsRange()).toBe("7d"));
    // Период сброшен (range ушёл как совпавший с дефолтом), team на месте.
    // Обновляет его само зеркало §76 — здесь важно лишь, что Overview его не съел.
    expect(new URLSearchParams(urlProbe.search).get("team")).toBe("alpha");
  });

  // Guard одноразового сброса: teamId приходит как "" → team-a, и наивная
  // проверка «id изменился» затёрла бы период из ссылки при обычном открытии.
  it("прямая ссылка ?range=30d уважается при первой загрузке", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "7d" }] });
    renderOverview(newClient(), "/?range=30d");

    await waitFor(() => expect(lastMetricsRange()).toBe("30d"));
    // Сброса не было: запрос ушёл ровно один раз и с периодом из ссылки.
    expect(metricsCalls).toHaveLength(1);
  });

  // «Не сохраняй в сеансе»: период не зеркалится в sessionStorage, поэтому
  // возврат на «Узлы» (URL без параметров) показывает дефолт команды.
  it("период не восстанавливается из сессионного зеркала", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "7d" }] });
    // Зеркало, как его мог оставить прошлый визит (в т.ч. старая версия SPA).
    sessionStorage.setItem("nexus.overview.filters", "q=foo&range=1h");

    renderOverview(newClient(), "/");

    await waitFor(() => expect(lastMetricsRange()).toBe("7d"));
    // Поиск из зеркала при этом восстановился — период исключение, а не правило.
    // Проверяем по полю ввода: URL живёт в MemoryRouter, а не в window.location.
    await waitFor(() =>
      expect((screen.getAllByRole("textbox")[0] as HTMLInputElement).value).toBe("foo"),
    );
  });

  // Анти-мигание: запрос метрик не должен уходить дважды (сначала с 24ч,
  // потом с настоящим дефолтом) — это двойная нагрузка на ClickHouse.
  it("метрики запрашиваются один раз и сразу с дефолтом команды", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "7d" }] });
    renderOverview(newClient());

    await waitFor(() => expect(metricsCalls.length).toBe(1));
    expect(metricsCalls[0]).toEqual({ range: "7d" });
  });

  it("пока префы едут, переключатель периода не отрисован и метрики не запрошены", () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [] });
    apiGet.mockImplementation((url: string) => {
      if (url === "/api/me/prefs") return new Promise(() => {}); // висит
      if (url === "/api/me/teams") {
        return Promise.resolve({ items: TEAMS, current_team_id: TEAM_A, favorites: [] });
      }
      return Promise.resolve({ items: [] });
    });
    renderOverview(newClient());

    expect(screen.queryByText("metrics.range.24h")).toBeNull();
    expect(metricsCalls).toHaveLength(0);
  });

  // Регресс на G.2: недоступные префы не должны лишать рабочий стол метрик.
  // Гейт по isSuccess вместо !isPending навсегда оставил бы страницу пустой.
  it("сбой /api/me/prefs не мешает загрузке метрик (дефолт 24ч)", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [], prefsFail: true });
    renderOverview(newClient());

    await waitFor(() => expect(metricsCalls.length).toBe(1));
    expect(metricsCalls[0]).toEqual({ range: "24h" });
  });

  it("клик по звёздочке сохраняет период как дефолт ЭТОЙ команды", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "24h" }] });
    renderOverview(newClient(), "/?range=1h");

    await waitFor(() => expect(lastMetricsRange()).toBe("1h"));
    fireEvent.click(screen.getByText("overview.set_default_period"));

    await waitFor(() =>
      expect(apiPut).toHaveBeenCalledWith("/api/me/prefs", {
        team_id: TEAM_A,
        key: PREF_KEY_OVERVIEW_PERIOD,
        value: { kind: "preset", range: "1h" },
      }),
    );
  });

  // Регресс: правка другого фильтра, пока префы едут, не должна стирать из URL
  // явно выбранный период, совпавший с системными 24ч (§71, ревизия).
  it("правка фильтра до загрузки префов не стирает явный range=24h", async () => {
    let releasePrefs: (v: unknown) => void = () => {};
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "7d" }] });
    const realGet = apiGet.getMockImplementation()!;
    apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
      if (url === "/api/me/prefs") {
        return new Promise((res) => {
          releasePrefs = () => res({ items: [prefItem(TEAM_A, "7d")] });
        });
      }
      return realGet(url, params);
    });

    renderOverview(newClient(), "/?range=24h");

    // Префы ещё едут: PeriodPicker скрыт, но фильтры доступны. Ищем именно
    // селект статуса (на странице их два — метод и статус).
    const statusSelect = screen
      .getAllByRole("combobox")
      .find((s) => s.querySelector('option[value="err"]'));
    fireEvent.change(statusSelect!, { target: { value: "err" } });

    releasePrefs(null);
    // Период остался выбранным пользователем (24ч), а не сменился на дефолт 7д.
    await waitFor(() => expect(lastMetricsRange()).toBe("24h"));
  });

  it("пустой набор префов не подменяется локальным значением", async () => {
    mockServer({ currentTeamID: TEAM_A, prefs: [] });
    const qc = newClient();
    renderOverview(qc);

    await waitFor(() => expect(lastMetricsRange()).toBe("24h"));
    expect(qc.getQueryData(ME_PREFS_KEY)).toEqual({ items: [] });
  });
});

// §92: кнопка «По умолчанию» в сквозном режиме «Все команды».
//
// Регресс, красный на прежнем коде: кнопки там не было вовсе (условие показа
// содержало !allTeams, §86.7), поэтому настроить стартовый период сквозного
// экрана было нечем, а сам он брал дефолт команды сессии — чужой для выдачи по
// всем командам.
describe("Overview: дефолтный период в сквозном режиме (§92)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
    setTeamScopeAll(true);
  });

  afterEach(() => setTeamScopeAll(false));

  it("берёт ГЛОБАЛЬНЫЙ преф, а не дефолт команды сессии", async () => {
    mockServer({
      currentTeamID: TEAM_A,
      prefs: [
        { team_id: TEAM_A, range: "1h" }, // дефолт команды сессии — не про этот экран
        { team_id: "", range: "7d" }, // глобальный — его и ждём
      ],
    });
    renderOverview(newClient());

    await waitFor(() => expect(lastMetricsRange()).toBe("7d"));
  });

  it("кнопка видна и сохраняет период ГЛОБАЛЬНО (пустой team_id)", async () => {
    // Глобального префа нет → дефолт 24ч; период берём из адреса, иначе кнопка
    // законно неактивна (текущий период уже равен сохранённому).
    mockServer({ currentTeamID: TEAM_A, prefs: [{ team_id: TEAM_A, range: "1h" }] });
    renderOverview(newClient(), "/?range=30d");

    const btn = await screen.findByText("overview.set_default_period");
    fireEvent.click(btn);

    await waitFor(() =>
      expect(apiPut).toHaveBeenCalledWith("/api/me/prefs", {
        team_id: "",
        key: PREF_KEY_OVERVIEW_PERIOD,
        value: { kind: "preset", range: "30d" },
      }),
    );
  });
});
