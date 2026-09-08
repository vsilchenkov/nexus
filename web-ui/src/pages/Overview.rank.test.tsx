import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import Overview from "./Overview";
import { PREF_KEY_OVERVIEW_PERIOD } from "../lib/prefs";
import { setTeamScopeAll } from "../lib/teamScope";
import { type MyTeamsResp } from "../lib/teams";

// §86.10: срез из шапки — базовый слой метрик сквозного режима.
//
// Два разных обещания проверяются здесь вместе, потому что оба держатся на нём:
//   1) статус узла честен для ВСЕГО списка, а не только для проскроленного;
//   2) порядок «проблемные первыми» строится один раз и замораживается.
//
// В jsdom нет IntersectionObserver, и порционная загрузка (useVisibleNodeMetrics)
// молчит по своей же защите. Это ровно тот случай, который проверяем: до
// прокрутки единственный источник метрик — срез.

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

// Узлы намеренно в алфавитном порядке путей и команд: прежний стабильный
// порядок (команда, затем путь) совпадает с порядком этого списка, поэтому
// «проблемные первыми» от него отличимо.
const NODES = [
  { id: "n1", path: "alpha/a", team_id: TEAM_A, root_method: "request", status: "active" },
  { id: "n2", path: "alpha/b", team_id: TEAM_A, root_method: "request", status: "active" },
  { id: "n3", path: "beta/c", team_id: TEAM_B, root_method: "request", status: "active" },
];

type Rank = { node_id: string; in: number; out: number; errors: number; last_outcome: string };

// slice — текущий срез «бэкенда»; тесты его подменяют между обновлениями.
let slice: Rank[] | undefined;
let totalsCalls = 0;
// chunkCalls — значения node_ids каждого порционного запроса метрик.
let chunkCalls: string[] = [];

// okRow / downRow — строки среза: узел работает / узел лежит.
function okRow(id: string, inCount: number): Rank {
  return { node_id: id, in: inCount, out: inCount, errors: 0, last_outcome: "ok" };
}
function downRow(id: string, inCount: number): Rank {
  return { node_id: id, in: inCount, out: 0, errors: inCount, last_outcome: "down" };
}

function mockServer() {
  totalsCalls = 0;
  chunkCalls = [];
  apiGet.mockImplementation((url: string, params?: Record<string, unknown>) => {
    if (url === "/api/me/teams") {
      const resp: MyTeamsResp = { items: TEAMS, current_team_id: TEAM_A, favorites: [] };
      return Promise.resolve(resp);
    }
    if (url === "/api/me/prefs") {
      return Promise.resolve({
        items: [
          {
            team_id: TEAM_A,
            key: PREF_KEY_OVERVIEW_PERIOD,
            value: { kind: "preset", range: "24h" },
            updated_at: "",
          },
        ],
      });
    }
    if (url === "/api/nodes") {
      // Сервер сужает список поиском — клиент получает УЖЕ отфильтрованное.
      // Фильтруем по РЕАЛЬНОМУ параметру запроса, а не по переменной теста:
      // иначе легко проверить сценарий, которого на клиенте не происходило.
      const q = String((params as Record<string, unknown>)?.search ?? "");
      return Promise.resolve({ items: q ? NODES.filter((n) => n.path.includes(q)) : NODES });
    }
    if (url === "/api/metrics/overview") {
      return Promise.resolve({
        incoming_24h: 0, outgoing_24h: 0, kafka_queue: 0,
        errors_24h: 0, error_rate: 0, prometheus_available: true,
      });
    }
    if (url === "/api/metrics/totals") {
      totalsCalls++;
      return Promise.resolve({
        totals: { incoming: 0, outgoing: 0, errors: 0, error_rate: 0 },
        nodes: slice,
      });
    }
    if (url === "/api/metrics/nodes") {
      chunkCalls.push(String((params as Record<string, unknown>)?.node_ids ?? ""));
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
  return new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
}

function renderOverview(qc: QueryClient, entry = "/") {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<Overview />, { wrapper });
}

// pathOrder — узлы в том порядке, в каком они отрисованы.
//
// Порядок читаем по ссылкам на узлы: и таблица, и карточки рисуют путь ссылкой
// на /nodes/:id, и это единственное, что в обоих представлениях одинаково.
// «+ Новый узел» ведёт туда же (/nodes/new) и в список попадать не должен.
function pathOrder(): string[] {
  return screen
    .getAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "")
    .filter((h) => h.startsWith("/nodes/"))
    .map((h) => h.slice("/nodes/".length))
    .filter((id) => id !== "new");
}

// statusPills — подписи бейджей статуса в порядке отрисовки.
//
// Отсеиваем <option>: у выпадающего списка фильтра подписи ТЕ ЖЕ ключи, и
// наивный getByText("overview.status.down") находит опцию мгновенно — на первом
// рендере, ещё до прихода данных. Проверка при этом зеленеет, не проверив ничего.
function statusPills(): string[] {
  return screen
    .getAllByText(/^overview\.status\./)
    .filter((el) => el.tagName !== "OPTION")
    .map((el) => el.textContent ?? "");
}

describe("Overview: срез и порядок в сквозном режиме (§86.10)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
    sessionStorage.clear();
    slice = [okRow("n1", 10), okRow("n2", 20), okRow("n3", 30)];
    mockServer();
    setTeamScopeAll(true);
  });

  afterEach(() => setTeamScopeAll(false));

  // Базовый слой. На прежнем коде статус выводился только из порционных пачек, а
  // они без прокрутки пусты — весь список стоял серым «неизвестно».
  it("статус берётся из среза для всего списка, а не только для проскроленного", async () => {
    slice = [downRow("n1", 10), okRow("n2", 20), okRow("n3", 30)];
    renderOverview(newClient());

    // Ждём прихода СРЕЗА, и оба условия сразу: строки появляются вместе со
    // списком узлов (/api/nodes) заметно раньше метрик, а «нет серых» на пустом
    // списке верно само по себе — по отдельности каждая проверка зеленела бы,
    // ничего не проверив.
    await waitFor(() => {
      const p = statusPills();
      expect(p).toHaveLength(3);
      expect(p).not.toContain("overview.status.unknown");
    });

    const pills = statusPills();
    expect(pills.filter((p) => p === "overview.status.down")).toHaveLength(1);
    expect(pills.filter((p) => p === "overview.status.ok")).toHaveLength(2);
  });

  // Порядок: проблемный узел уезжает наверх, хотя по прежнему правилу (команда,
  // затем путь) он стоял бы последним.
  it("проблемные встают первыми", async () => {
    slice = [okRow("n1", 10), okRow("n2", 20), downRow("n3", 5)];
    renderOverview(newClient());

    await waitFor(() => expect(pathOrder()).toEqual(["n3", "n2", "n1"]));
  });

  // Внутри одного ранга — по убыванию входящих (§22).
  it("внутри ранга сортирует по убыванию входящих", async () => {
    slice = [okRow("n1", 10), okRow("n2", 30), okRow("n3", 20)];
    renderOverview(newClient());

    await waitFor(() => expect(pathOrder()).toEqual(["n2", "n3", "n1"]));
  });

  // Заморозка. Главное обещание §86.10: автообновление меняет числа и бейджи, но
  // НЕ позиции — иначе строки уезжают из-под курсора, ради чего порядок и
  // замораживали.
  it("второй срез с другими числами порядок не меняет", async () => {
    slice = [okRow("n1", 10), okRow("n2", 30), okRow("n3", 20)];
    const qc = newClient();
    renderOverview(qc);

    await waitFor(() => expect(pathOrder()).toEqual(["n2", "n3", "n1"]));

    // Тот же скоуп и период, новые числа: n1 стал самым нагруженным и лёг.
    slice = [downRow("n1", 99), okRow("n2", 1), okRow("n3", 2)];
    await qc.refetchQueries({ queryKey: ["metrics-totals"] });

    // Бейдж обязан обновиться...
    //
    // Проверяем через statusPills (он отсеивает <option>), а не getByText:
    // подпись пункта фильтра — тот же ключ, и наивный поиск находил ИМЕННО
    // опцию, зеленея ещё до появления бейджа. Ровно та ловушка, о которой
    // предупреждает комментарий к statusPills выше.
    await waitFor(() => expect(statusPills()).toContain("overview.status.down"));
    // ...а порядок — остаться прежним.
    expect(pathOrder()).toEqual(["n2", "n3", "n1"]);
  });

  // Смена периода — новый ключ заморозки: порядок пересчитывается.
  it("смена периода перестраивает порядок", async () => {
    slice = [okRow("n1", 10), okRow("n2", 30), okRow("n3", 20)];
    const qc = newClient();
    const { unmount } = renderOverview(qc, "/?range=24h");

    await waitFor(() => expect(pathOrder()).toEqual(["n2", "n3", "n1"]));

    slice = [downRow("n1", 1), okRow("n2", 30), okRow("n3", 20)];
    unmount();
    renderOverview(qc, "/?range=7d");

    await waitFor(() => expect(pathOrder()).toEqual(["n1", "n2", "n3"]));
  });

  // §86.11: «Обновить» — единственный способ перестроить порядок, не трогая
  // период и скоуп. Без него заморозка была бы ловушкой: оператор видит
  // устаревший порядок и ничего не может с ним сделать.
  it("кнопка «Обновить» перестраивает порядок", async () => {
    slice = [okRow("n1", 10), okRow("n2", 30), okRow("n3", 20)];
    renderOverview(newClient());

    await waitFor(() => expect(pathOrder()).toEqual(["n2", "n3", "n1"]));

    slice = [downRow("n1", 1), okRow("n2", 30), okRow("n3", 20)];
    fireEvent.click(screen.getByRole("button", { name: "overview.refresh" }));

    await waitFor(() => expect(pathOrder()).toEqual(["n1", "n2", "n3"]));
  });

  // Кнопка обязана работать и на паузе — там она вообще единственный способ
  // обновиться.
  it("«Обновить» перезапрашивает срез при выключенном автообновлении", async () => {
    localStorage.setItem("nexus.overview.autorefresh", "0");
    renderOverview(newClient());

    await waitFor(() => expect(totalsCalls).toBe(1));
    // Убеждаемся, что режим действительно «Пауза», а не просто интервал не успел
    // сработать: иначе тест проверял бы обычное автообновление.
    expect(screen.getByText("overview.autorefresh_off")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "overview.refresh" }));

    await waitFor(() => expect(totalsCalls).toBe(2));
    // Клик по «Обновить» паузу не снимает.
    expect(screen.getByText("overview.autorefresh_off")).toBeInTheDocument();
  });

  // Природа элемента, а не только реакция на клик (урок §79): у иконки без
  // подписи доступное имя обязано браться из aria-label, иначе кнопка немая
  // для скринридера и не находится по имени вовсе.
  it("«Обновить» — кнопка с доступным именем и без подписи", async () => {
    renderOverview(newClient());

    const btn = await screen.findByRole("button", { name: "overview.refresh" });
    expect(btn.tagName).toBe("BUTTON");
    expect(btn.textContent).toBe("");
  });

  // Найдено на стенде: одно касание фильтра по статусу добавляло в порционную
  // загрузку ВЕСЬ список узлов (preload), и дальше он пересчитывался в
  // ClickHouse каждые 12 секунд — даже после сброса фильтра, потому что пачки
  // заморожены и не удаляются. Со срезом preload не нужен: статус всех узлов
  // уже известен.
  it("фильтр по статусу не тянет метрики всего списка, когда есть срез", async () => {
    slice = [downRow("n1", 10), okRow("n2", 20), okRow("n3", 30)];
    renderOverview(newClient(), "/?status=err");

    await waitFor(() => expect(pathOrder()).toEqual(["n1"]));
    // Ни одного запроса метрик с перечислением узлов: фильтр обслужен срезом.
    expect(chunkCalls).toEqual([]);
  });

  // Обратная сторона того же гейта: без среза preload обязан работать, иначе
  // фильтр по статусу снова замкнётся в круг и покажет пусто навсегда.
  it("без среза фильтр по статусу по-прежнему догружает метрики кандидатов", async () => {
    slice = undefined;
    renderOverview(newClient(), "/?status=err");

    await waitFor(() => expect(chunkCalls.length).toBeGreaterThan(0));
    expect(chunkCalls.join(",")).toContain("n1");
  });

  // Найдено ревизией: карта рангов строится из списка узлов, а тот приходит
  // УЖЕ отфильтрованным поиском (`/api/nodes?search=`). Если поиск не входит в
  // ключ заморозки, то открыв страницу по ссылке с поиском и сбросив его,
  // оператор получал бы наверху горстку найденных ранее узлов, а всё остальное
  // — алфавитом: в карте их просто нет.
  it("после сброса поиска ранжируется весь список, а не только найденное ранее", async () => {
    slice = [okRow("n1", 10), okRow("n2", 30), downRow("n3", 5)];
    // Открываемся по ссылке с поиском: список узлов приходит суженным.
    renderOverview(newClient(), "/?q=alpha%2Fb");

    await waitFor(() => expect(pathOrder()).toEqual(["n2"]));

    // Оператор очищает строку поиска — как в браузере, а не пересозданием
    // страницы: поиск переживает перемонтирование через зеркало сессии (§54).
    fireEvent.change(screen.getAllByRole("textbox")[0], { target: { value: "" } });

    await waitFor(() => expect(pathOrder()).toEqual(["n3", "n2", "n1"]), { timeout: 3000 });
  });

  // Деградация: старый бэкенд среза не отдаёт. Экран обязан пережить это со
  // прежним стабильным порядком, а не рассыпаться.
  it("без среза остаётся стабильный порядок (команда, затем путь)", async () => {
    slice = undefined;
    renderOverview(newClient());

    await waitFor(() => expect(totalsCalls).toBeGreaterThan(0));
    await waitFor(() => expect(pathOrder()).toEqual(["n1", "n2", "n3"]));
  });
});
