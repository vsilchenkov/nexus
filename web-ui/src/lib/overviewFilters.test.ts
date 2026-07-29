import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";

import {
  applyFilters,
  defaultFilters,
  hasFilterParams,
  loadFilters,
  parseFilters,
  saveFilters,
  serializeFilters,
  type OverviewFilters,
} from "./overviewFilters";
import { defaultPeriod, type Period } from "./period";

// §54: фильтры рабочего стола — URL источник истины, sessionStorage зеркало.
// §71: дефолтный период приходит параметром (он живёт на сервере и свой у
// каждой команды), поэтому модуль больше не читает хранилище сам — чистим
// только зеркало.
beforeEach(() => {
  sessionStorage.clear();
});

const custom = (from: string, to: string): Period => ({ kind: "custom", from, to });
const qs = (s: string) => new URLSearchParams(s);
const sevenDays: Period = { kind: "preset", range: "7d" };

// Хелперы с системным дефолтом по умолчанию: подавляющее большинство кейсов
// проверяет разбор параметров, а не резолв дефолта.
const parse = (s: string, def: Period = defaultPeriod) => parseFilters(qs(s), def);
const ser = (f: OverviewFilters, def: Period = defaultPeriod) => serializeFilters(f, def);
const def = (d: Period = defaultPeriod) => defaultFilters(d);

describe("parseFilters", () => {
  it("returns defaults for empty params", () => {
    expect(parse("")).toEqual({
      search: "",
      method: "",
      status: "all",
      period: defaultPeriod,
    });
  });

  it("takes the per-team default period (§71) as the period default", () => {
    expect(parse("", sevenDays).period).toEqual(sevenDays);
  });

  it("parses a full query", () => {
    expect(parse("q=foo&method=request&status=err&range=7d")).toEqual({
      search: "foo",
      method: "request",
      status: "err",
      period: sevenDays,
    });
  });

  // Явно выбранный период сильнее дефолта команды — на этом держится решение
  // «ручной выбор переживает переключение команды» (§71).
  it("an explicit range wins over the team default", () => {
    expect(parse("range=30d", sevenDays).period).toEqual({ kind: "preset", range: "30d" });
  });

  it.each([
    ["method=DELETE", "method", ""],
    ["method=", "method", ""],
    ["status=banana", "status", "all"],
    ["status=", "status", "all"],
  ])("junk %s falls back per-field", (query, field, want) => {
    expect(parse(query)[field as "method" | "status"]).toBe(want);
  });

  it("keeps valid fields when a neighbour is junk", () => {
    const f = parse("status=banana&q=foo&range=99h");
    expect(f).toEqual({ search: "foo", method: "", status: "all", period: defaultPeriod });
  });

  it("parses a custom period", () => {
    expect(parse("from=2026-07-01T00:00:00Z&to=2026-07-02T00:00:00Z").period).toEqual(
      custom("2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"),
    );
  });

  it.each([
    ["from without to", "from=2026-07-01T00:00:00Z"],
    ["to without from", "to=2026-07-02T00:00:00Z"],
    ["unparsable from", "from=junk&to=2026-07-02T00:00:00Z"],
    ["unparsable to", "from=2026-07-01T00:00:00Z&to=junk"],
    ["unknown preset", "range=99h"],
  ])("%s → default period", (_name, query) => {
    expect(parse(query).period).toEqual(defaultPeriod);
  });

  it("prefers range over from/to when both are present", () => {
    const f = parse("range=3h&from=2026-07-01T00:00:00Z&to=2026-07-02T00:00:00Z");
    expect(f.period).toEqual({ kind: "preset", range: "3h" });
  });
});

describe("serializeFilters", () => {
  it("writes nothing when everything is default", () => {
    expect(ser(def()).toString()).toBe("");
  });

  it("omits the period when it equals the per-team default (§71)", () => {
    const f: OverviewFilters = { ...def(sevenDays), period: sevenDays };
    expect(ser(f, sevenDays).has("range")).toBe(false);
  });

  // Тот же период при ДРУГОМ дефолте команды уже не дефолтный и обязан
  // попасть в URL — иначе он потерялся бы при переключении команды.
  it("writes the same preset when the team default differs", () => {
    const f: OverviewFilters = { ...def(), period: sevenDays };
    expect(ser(f, defaultPeriod).toString()).toBe("range=7d");
  });

  it("writes a non-default preset as range", () => {
    const f: OverviewFilters = { ...def(), period: { kind: "preset", range: "1h" } };
    expect(ser(f).toString()).toBe("range=1h");
  });

  it("writes a custom period as from/to without range", () => {
    const f: OverviewFilters = {
      ...def(),
      period: custom("2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"),
    };
    const out = ser(f);
    expect(out.get("from")).toBe("2026-07-01T00:00:00Z");
    expect(out.get("to")).toBe("2026-07-02T00:00:00Z");
    expect(out.has("range")).toBe(false);
  });

  it.each([
    [{ search: "foo" }, "q=foo"],
    [{ method: "requestAsync" as const }, "method=requestAsync"],
    [{ status: "degraded" as const }, "status=degraded"],
  ])("writes only the non-default field %o", (patch, want) => {
    expect(ser({ ...def(), ...patch }).toString()).toBe(want);
  });

  // Регресс (§71): пока дефолт команды не загружен, «дефолтность» периода
  // определять нечем. Если считать дефолтом системные 24ч, то правка любого
  // другого фильтра в это окно стёрла бы из URL явно выбранный range=24h, и
  // после прихода префов период молча сменился бы на дефолт команды.
  it("writes the period always when the team default is unknown (null)", () => {
    const f: OverviewFilters = { ...def(), status: "err" };
    const out = serializeFilters(f, null);
    expect(out.get("range")).toBe("24h");
    expect(out.get("status")).toBe("err");
  });

  it("writes a custom period when the team default is unknown (null)", () => {
    const f: OverviewFilters = {
      ...def(),
      period: custom("2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"),
    };
    const out = serializeFilters(f, null);
    expect(out.get("from")).toBe("2026-07-01T00:00:00Z");
    expect(out.has("range")).toBe(false);
  });
});

describe("round-trip", () => {
  it.each([
    ["all defaults", def()],
    ["search only", { ...def(), search: "da" }],
    [
      "everything set",
      {
        search: "da",
        method: "RabbitMQAsync" as const,
        status: "err" as const,
        period: { kind: "preset", range: "1h" } as Period,
      },
    ],
    ["custom period", { ...def(), period: custom("2026-07-01T10:30:00Z", "2026-07-02T11:45:00Z") }],
    ["search with spaces and plus", { ...def(), search: "a b+c" }],
  ])("parse(serialize(f)) === f — %s", (_name, f) => {
    expect(parseFilters(ser(f as OverviewFilters), defaultPeriod)).toEqual(f);
  });

  it("round-trips against a non-system team default", () => {
    const f: OverviewFilters = { ...def(sevenDays), search: "da" };
    expect(parseFilters(ser(f, sevenDays), sevenDays)).toEqual(f);
  });
});

describe("hasFilterParams", () => {
  it.each([
    ["", false],
    ["utm=1", false],
    ["q=foo", true],
    ["status=err", true],
    ["from=2026-07-01T00:00:00Z", true],
  ])("%s → %s", (query, want) => {
    expect(hasFilterParams(qs(query))).toBe(want);
  });
});

describe("applyFilters", () => {
  it("keeps foreign params and overwrites its own", () => {
    const prev = qs("utm=1&q=old&status=err");
    const out = applyFilters(prev, { ...def(), search: "new" }, defaultPeriod);
    expect(out.get("utm")).toBe("1");
    expect(out.get("q")).toBe("new");
    expect(out.has("status")).toBe(false); // вернулся к дефолту → ключ удалён
  });

  it("clears its own params when filters are default", () => {
    const out = applyFilters(qs("utm=1&q=old&range=1h"), def(), defaultPeriod);
    expect(out.toString()).toBe("utm=1");
  });

  // Выбор периода, равного дефолту команды, убирает range из URL: ровно так
  // «липкий» ручной выбор возвращается к дефолту (§71.6).
  it("drops range when the period matches the team default", () => {
    const out = applyFilters(qs("range=1h"), { ...def(sevenDays), period: sevenDays }, sevenDays);
    expect(out.has("range")).toBe(false);
  });
});

describe("saveFilters / loadFilters", () => {
  it("round-trips through the mirror", () => {
    const f: OverviewFilters = {
      search: "da",
      method: "request",
      status: "err",
      period: { kind: "preset", range: "1h" },
    };
    saveFilters(f, defaultPeriod);
    expect(loadFilters(defaultPeriod)).toEqual(f);
  });

  it("removes the key when everything is default (§54.4: cleared means nothing to restore)", () => {
    saveFilters({ ...def(), search: "da" }, defaultPeriod);
    expect(sessionStorage.getItem("nexus.overview.filters")).not.toBeNull();

    saveFilters(def(), defaultPeriod);
    expect(sessionStorage.getItem("nexus.overview.filters")).toBeNull();
    expect(loadFilters(defaultPeriod)).toBeNull();
  });

  it("returns null on an empty mirror", () => {
    expect(loadFilters(defaultPeriod)).toBeNull();
  });

  it.each([
    ["garbage==&&", null],
    ["status=banana&method=DELETE", null], // всё junk → всё-дефолт → нечего восстанавливать
  ])("tolerates junk in the mirror: %s", (raw, want) => {
    sessionStorage.setItem("nexus.overview.filters", raw);
    expect(loadFilters(defaultPeriod)).toBe(want);
  });

  it("sanitizes partially-junk mirror content", () => {
    sessionStorage.setItem("nexus.overview.filters", "q=foo&status=banana");
    expect(loadFilters(defaultPeriod)).toEqual({ ...def(), search: "foo" });
  });
});

describe("storage failures (private mode)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("saveFilters does not throw when the storage throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    expect(() => saveFilters({ ...def(), search: "da" }, defaultPeriod)).not.toThrow();
  });

  it("loadFilters returns null when the storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    expect(loadFilters(defaultPeriod)).toBeNull();
  });
});
