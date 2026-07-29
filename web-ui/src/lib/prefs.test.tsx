import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  ME_PREFS_KEY,
  PREF_KEY_OVERVIEW_PERIOD,
  useMigrateLegacyPeriodPref,
  useSetPref,
  useTeamDefaultPeriod,
  type UserPref,
} from "./prefs";

// §71: дефолтный период рабочего стола хранится на сервере и разрешается как
// «преф команды → глобальный преф → системные 24ч».

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, put: apiPut } };
});

const TEAM_A = "team-a";
const TEAM_B = "team-b";
const LEGACY_KEY = "nexus.overview.period";

function pref(teamID: string, range: string): UserPref {
  return {
    team_id: teamID,
    key: PREF_KEY_OVERVIEW_PERIOD,
    value: { kind: "preset", range },
    updated_at: "2026-07-29T10:00:00Z",
  };
}

// mockPrefs — GET /api/me/prefs отдаёт заданный набор; PUT просто успешен.
function mockPrefs(items: UserPref[]) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/me/prefs") return Promise.resolve({ items });
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  apiPut.mockResolvedValue({});
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function wrapperFor(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

describe("useTeamDefaultPeriod", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
  });

  it("преф команды выигрывает у глобального", async () => {
    mockPrefs([pref("", "24h"), pref(TEAM_A, "7d")]);
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "7d" });
  });

  it("без командного префа берётся глобальный", async () => {
    mockPrefs([pref("", "30d"), pref(TEAM_B, "1h")]);
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "30d" });
  });

  it("без префов вообще — системные 24ч", async () => {
    mockPrefs([]);
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "24h" });
  });

  it.each([
    ["неизвестный пресет", { kind: "preset", range: "99h" }],
    ["произвольный период", { kind: "custom", from: "2026-01-01", to: "2026-01-02" }],
    ["строка", "7d"],
    ["null", null],
  ])("мусор в значении префа (%s) откатывается на 24ч", async (_name, value) => {
    mockPrefs([{ team_id: TEAM_A, key: PREF_KEY_OVERVIEW_PERIOD, value, updated_at: "" }]);
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "24h" });
  });

  it("settled=false пока запрос не завершён", () => {
    apiGet.mockImplementation(() => new Promise(() => {})); // никогда не резолвится
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    expect(result.current.settled).toBe(false);
  });

  // Регресс на G.2: гейт по isSuccess навсегда оставил бы рабочий стол без
  // метрик при недоступном эндпоинте — настройка UI уронила бы страницу.
  it("settled=true и при ошибке запроса (эндпоинт недоступен)", async () => {
    apiGet.mockRejectedValue({ response: { status: 500 } });
    const { result } = renderHook(() => useTeamDefaultPeriod(TEAM_A), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "24h" });
  });

  it("teamId пустой (членства ещё не загрузились) — берётся глобальный преф", async () => {
    mockPrefs([pref("", "3h"), pref(TEAM_A, "7d")]);
    const { result } = renderHook(() => useTeamDefaultPeriod(""), {
      wrapper: wrapperFor(newClient()),
    });

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(result.current.value).toEqual({ kind: "preset", range: "3h" });
  });
});

describe("useSetPref", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
  });

  it("optimistic: кеш обновляется до ответа сервера", async () => {
    mockPrefs([pref(TEAM_A, "24h")]);
    const qc = newClient();
    let resolvePut: (v: unknown) => void = () => {};
    apiPut.mockImplementation(() => new Promise((res) => (resolvePut = res)));

    const { result } = renderHook(
      () => ({ set: useSetPref(), def: useTeamDefaultPeriod(TEAM_A) }),
      { wrapper: wrapperFor(qc) },
    );
    await waitFor(() => expect(result.current.def.settled).toBe(true));

    result.current.set.mutate({
      teamId: TEAM_A,
      key: PREF_KEY_OVERVIEW_PERIOD,
      value: { kind: "preset", range: "1h" },
    });

    await waitFor(() =>
      expect(result.current.def.value).toEqual({ kind: "preset", range: "1h" }),
    );
    resolvePut({});
  });

  it("откат кеша при ошибке PUT", async () => {
    mockPrefs([pref(TEAM_A, "24h")]);
    apiPut.mockRejectedValue({ response: { status: 400 } });
    const qc = newClient();

    const { result } = renderHook(
      () => ({ set: useSetPref(), def: useTeamDefaultPeriod(TEAM_A) }),
      { wrapper: wrapperFor(qc) },
    );
    await waitFor(() => expect(result.current.def.settled).toBe(true));

    result.current.set.mutate({
      teamId: TEAM_A,
      key: PREF_KEY_OVERVIEW_PERIOD,
      value: { kind: "preset", range: "1h" },
    });

    await waitFor(() => expect(result.current.set.isError).toBe(true));
    await waitFor(() =>
      expect(result.current.def.value).toEqual({ kind: "preset", range: "24h" }),
    );
  });
});

describe("useMigrateLegacyPeriodPref", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    localStorage.clear();
  });

  it("переносит старый localStorage-ключ в глобальный преф и очищает его", async () => {
    localStorage.setItem(LEGACY_KEY, JSON.stringify({ kind: "preset", range: "7d" }));
    mockPrefs([]);

    renderHook(() => useMigrateLegacyPeriodPref(), { wrapper: wrapperFor(newClient()) });

    await waitFor(() => expect(apiPut).toHaveBeenCalledTimes(1));
    expect(apiPut).toHaveBeenCalledWith("/api/me/prefs", {
      team_id: "",
      key: PREF_KEY_OVERVIEW_PERIOD,
      value: { kind: "preset", range: "7d" },
    });
    await waitFor(() => expect(localStorage.getItem(LEGACY_KEY)).toBeNull());
  });

  // Иначе заход со старого браузера затёр бы настройку, сделанную с рабочей
  // машины (G.5).
  it("не трогает сервер, если глобальный преф уже есть", async () => {
    localStorage.setItem(LEGACY_KEY, JSON.stringify({ kind: "preset", range: "7d" }));
    mockPrefs([pref("", "1h")]);

    const { result } = renderHook(
      () => {
        useMigrateLegacyPeriodPref();
        return useTeamDefaultPeriod(TEAM_A);
      },
      { wrapper: wrapperFor(newClient()) },
    );

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(apiPut).not.toHaveBeenCalled();
    expect(localStorage.getItem(LEGACY_KEY)).not.toBeNull();
  });

  it("при ошибке PUT ключ остаётся — миграция повторится позже", async () => {
    localStorage.setItem(LEGACY_KEY, JSON.stringify({ kind: "preset", range: "7d" }));
    mockPrefs([]);
    apiPut.mockRejectedValue({ response: { status: 500 } });

    renderHook(() => useMigrateLegacyPeriodPref(), { wrapper: wrapperFor(newClient()) });

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(localStorage.getItem(LEGACY_KEY)).not.toBeNull();
  });

  it("мусор в старом ключе не мигрирует", async () => {
    localStorage.setItem(LEGACY_KEY, "{not json");
    mockPrefs([]);

    const { result } = renderHook(
      () => {
        useMigrateLegacyPeriodPref();
        return useTeamDefaultPeriod(TEAM_A);
      },
      { wrapper: wrapperFor(newClient()) },
    );

    await waitFor(() => expect(result.current.settled).toBe(true));
    expect(apiPut).not.toHaveBeenCalled();
  });

  it("не мигрирует, пока префы не загружены", () => {
    localStorage.setItem(LEGACY_KEY, JSON.stringify({ kind: "preset", range: "7d" }));
    apiGet.mockImplementation(() => new Promise(() => {}));

    renderHook(() => useMigrateLegacyPeriodPref(), { wrapper: wrapperFor(newClient()) });

    expect(apiPut).not.toHaveBeenCalled();
  });
});

describe("ME_PREFS_KEY", () => {
  it("совпадает с ключом в TEAM_INDEPENDENT_KEYS", async () => {
    const { TEAM_INDEPENDENT_KEYS } = await import("./teams");
    expect(TEAM_INDEPENDENT_KEYS.has(String(ME_PREFS_KEY[0]))).toBe(true);
  });
});
