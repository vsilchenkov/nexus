import { describe, expect, it } from "vitest";

import { queryClient } from "./queryClient";
import { isNotFound } from "../api/client";

// retry-политика: 4xx не повторяем. Регрессия «кнопка Обновить висит вечно»:
// узел чужой команды отдаёт 404, три попытки с бэкоффом на каждый
// refetchInterval держали useIsFetching() > 0 практически постоянно.
describe("queryClient retry", () => {
  const retry = queryClient.getDefaultOptions().queries?.retry as (
    failureCount: number,
    error: unknown,
  ) => boolean;
  const httpErr = (status: number) => ({ response: { status } });

  it.each([400, 401, 403, 404, 409, 429])("не ретраит %i", (status) => {
    expect(retry(0, httpErr(status))).toBe(false);
  });

  it.each([500, 502, 503])("ретраит %i до двух раз", (status) => {
    expect(retry(0, httpErr(status))).toBe(true);
    expect(retry(1, httpErr(status))).toBe(true);
    expect(retry(2, httpErr(status))).toBe(false);
  });

  it("ретраит сетевую ошибку без ответа", () => {
    expect(retry(0, new Error("Network Error"))).toBe(true);
    expect(retry(2, new Error("Network Error"))).toBe(false);
  });
});

describe("isNotFound", () => {
  it.each([
    ["404 → true", { response: { status: 404 } }, true],
    ["403 → false", { response: { status: 403 } }, false],
    ["500 → false", { response: { status: 500 } }, false],
    ["сетевая ошибка → false", new Error("boom"), false],
    ["null → false", null, false],
    ["undefined → false", undefined, false],
  ])("%s", (_name, err, want) => {
    expect(isNotFound(err)).toBe(want);
  });
});
