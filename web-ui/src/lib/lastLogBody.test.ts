import { beforeEach, describe, expect, it, vi } from "vitest";

// §83.6 (боевой дефект): «Взять последний запрос из логов» на живом узле
// acs_sigur с включённым логированием отвечало «записей нет или логирование
// тела выключено». Причина — форма вызова: `api.get(url, params)` принимает
// карту параметров ВТОРЫМ аргументом, а передавали `{ params: {...} }`, из-за
// чего axios слал `?params[which]=request` и обязательный `which` до сервера
// не доходил → 400 → общий текст ошибки.
//
// Проверено на бою: запрос с `params%5Bwhich%5D=request` отвечает 400, тот же
// запрос с `which=request` — 200 с телом.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

import { fetchLastLogBody } from "./lastLogBody";

const BODY = '{"logs":[{"logId":79839}]}';

function bodyCall() {
  return apiGet.mock.calls.find((c) => String(c[0]).endsWith("/body"));
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockImplementation((url: string) => {
    if (url.endsWith("/logs")) return Promise.resolve({ items: [{ id: "log-1" }] });
    if (url.endsWith("/body")) return Promise.resolve({ chunk: BODY, eof: true, returned: BODY.length });
    return Promise.resolve({});
  });
});

describe("fetchLastLogBody (§83.6)", () => {
  it("шлёт which ПЛОСКИМ параметром — красный на старом коде (params[which])", async () => {
    await fetchLastLogBody("n1");
    const call = bodyCall();
    expect(call).toBeDefined();
    // Ключевая проверка: смотрим на фактические аргументы вызова. При старой
    // форме здесь лежало { params: { which: "request" } } и which был undefined.
    expect((call![1] as Record<string, unknown>).which).toBe("request");
    expect((call![1] as Record<string, unknown>).params).toBeUndefined();
  });

  it("limit списка тоже уходит плоско", async () => {
    await fetchLastLogBody("n1");
    const call = apiGet.mock.calls.find((c) => String(c[0]).endsWith("/logs"));
    expect((call![1] as Record<string, unknown>).limit).toBe(1);
    expect((call![1] as Record<string, unknown>).params).toBeUndefined();
  });

  it("возвращает тело последней записи", async () => {
    await expect(fetchLastLogBody("n1")).resolves.toEqual({ ok: true, body: BODY });
  });

  it("несохранённый узел — no_node, без единого запроса", async () => {
    await expect(fetchLastLogBody(undefined)).resolves.toEqual({ ok: false, reason: "no_node" });
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("журнал пуст — no_records, тело не запрашивается", async () => {
    apiGet.mockImplementation((url: string) =>
      url.endsWith("/logs") ? Promise.resolve({ items: [] }) : Promise.resolve({}),
    );
    await expect(fetchLastLogBody("n1")).resolves.toEqual({ ok: false, reason: "no_records" });
    expect(bodyCall()).toBeUndefined();
  });

  it("тело пустое — body_not_logged, а не «записей нет»", async () => {
    apiGet.mockImplementation((url: string) => {
      if (url.endsWith("/logs")) return Promise.resolve({ items: [{ id: "log-1" }] });
      return Promise.resolve({ chunk: "   " });
    });
    await expect(fetchLastLogBody("n1")).resolves.toEqual({ ok: false, reason: "body_not_logged" });
  });

  it("отказ сервера — request_failed, а не «логирование выключено»", async () => {
    apiGet.mockImplementation((url: string) => {
      if (url.endsWith("/logs")) return Promise.resolve({ items: [{ id: "log-1" }] });
      return Promise.reject(new Error("400"));
    });
    await expect(fetchLastLogBody("n1")).resolves.toEqual({ ok: false, reason: "request_failed" });
  });

  it("отказ на списке тоже request_failed", async () => {
    apiGet.mockImplementation(() => Promise.reject(new Error("500")));
    await expect(fetchLastLogBody("n1")).resolves.toEqual({ ok: false, reason: "request_failed" });
  });
});
