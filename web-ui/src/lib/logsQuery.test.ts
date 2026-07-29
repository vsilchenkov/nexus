import { describe, expect, it } from "vitest";

import {
  isLogOK,
  logsFilterParams,
  logsFilterSearch,
  type LogsFilterState,
} from "./logsQuery";

// §72.2: единый набор query-параметров фильтра логов. Главное, что проверяется, —
// быстрые status/done ВХОДЯТ в параметры: до §72.2 список логов их не слал и
// фильтровал уже загруженную страницу в браузере («Показано 3 из 33»).

const empty: LogsFilterState = {
  q: "",
  qCase: false,
  qWord: false,
  qRegex: false,
  method: "",
  clientHost: "",
  from: "",
  to: "",
  status: "all",
  done: "all",
};

function state(patch: Partial<LogsFilterState>): LogsFilterState {
  return { ...empty, ...patch };
}

describe("logsFilterParams", () => {
  it("пустой фильтр не даёт ни одного параметра", () => {
    expect(logsFilterParams(empty)).toEqual({});
  });

  const cases: { name: string; patch: Partial<LogsFilterState>; want: Record<string, string> }[] = [
    { name: "status=all не отправляется", patch: { status: "all" }, want: {} },
    { name: "status=err уходит на сервер", patch: { status: "err" }, want: { status: "err" } },
    { name: "status=ok уходит на сервер", patch: { status: "ok" }, want: { status: "ok" } },
    { name: "done=all не отправляется", patch: { done: "all" }, want: {} },
    { name: "done=pending → done=no", patch: { done: "pending" }, want: { done: "no" } },
    { name: "done=done → done=yes", patch: { done: "done" }, want: { done: "yes" } },
    { name: "method", patch: { method: "v1/orders" }, want: { method: "v1/orders" } },
    { name: "clientHost → client_host", patch: { clientHost: "srv-1c" }, want: { client_host: "srv-1c" } },
    { name: "поиск без режимов", patch: { q: "boom" }, want: { q: "boom" } },
    {
      name: "поиск с режимами Aa / ab| / .*",
      patch: { q: "boom", qCase: true, qWord: true, qRegex: true },
      want: { q: "boom", q_case: "1", q_word: "1", q_regex: "1" },
    },
    { name: "режимы без запроса не отправляются", patch: { qCase: true, qRegex: true }, want: {} },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(logsFilterParams(state(c.patch))).toEqual(c.want);
    });
  }

  it("даты переводятся в ISO", () => {
    const p = logsFilterParams(state({ from: "2026-07-21T10:00", to: "2026-07-21T11:30" }));
    expect(p.from).toBe(new Date("2026-07-21T10:00").toISOString());
    expect(p.to).toBe(new Date("2026-07-21T11:30").toISOString());
  });

  it("неразбираемая дата не отправляется и не роняет рендер", () => {
    // new Date("...").toISOString() на невалидном значении бросает RangeError и
    // уронил бы всю вкладку. Значение приходит из состояния компонента, поэтому
    // на него нельзя полагаться как на всегда корректное.
    const bad = state({ from: "2026-13-45T99:99" });
    expect(() => logsFilterParams(bad)).not.toThrow();
    expect(logsFilterParams(bad)).toEqual({});
  });

  it("комбинация быстрых и расширенных фильтров", () => {
    expect(
      logsFilterParams(state({ status: "err", done: "pending", method: "v1/push", q: "timeout" })),
    ).toEqual({ status: "err", done: "no", method: "v1/push", q: "timeout" });
  });
});

describe("logsFilterSearch", () => {
  it("пустой фильтр → пустая строка (URL стрима без ?)", () => {
    expect(logsFilterSearch(empty)).toBe("");
  });

  it("быстрые фильтры попадают в URL SSE-стрима", () => {
    const qs = logsFilterSearch(state({ status: "err", done: "pending" }));
    expect(qs.startsWith("?")).toBe(true);
    const params = new URLSearchParams(qs.slice(1));
    expect(params.get("status")).toBe("err");
    expect(params.get("done")).toBe("no");
  });
});

describe("isLogOK", () => {
  // Зеркало серверного предиката §72.1: ok и err обязаны покрывать всё без
  // пересечений, а 3xx (редирект §50) — это успех, а не «ни то, ни сё».
  const rows: { name: string; row: { done: boolean; status: number }; want: boolean }[] = [
    { name: "200 завершено", row: { done: true, status: 200 }, want: true },
    { name: "302 завершено (редирект)", row: { done: true, status: 302 }, want: true },
    { name: "399 завершено", row: { done: true, status: 399 }, want: true },
    { name: "400 завершено", row: { done: true, status: 400 }, want: false },
    { name: "500 завершено", row: { done: true, status: 500 }, want: false },
    { name: "200 не завершено", row: { done: false, status: 200 }, want: false },
    { name: "таймаут status=0", row: { done: false, status: 0 }, want: false },
  ];
  for (const r of rows) {
    it(r.name, () => expect(isLogOK(r.row)).toBe(r.want));
  }
});
