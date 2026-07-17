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
// Оба хранилища чистим: parse зависит от звёздочки §44.B (nexus.overview.period
// в localStorage), зеркало живёт в sessionStorage.
beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});

const custom = (from: string, to: string): Period => ({ kind: "custom", from, to });
const qs = (s: string) => new URLSearchParams(s);

describe("parseFilters", () => {
  it("returns defaults for empty params", () => {
    expect(parseFilters(qs(""))).toEqual({
      search: "",
      method: "",
      status: "all",
      period: defaultPeriod,
    });
  });

  it("takes the starred default period (§44.B) as the period default", () => {
    localStorage.setItem("nexus.overview.period", JSON.stringify({ kind: "preset", range: "7d" }));
    expect(parseFilters(qs("")).period).toEqual({ kind: "preset", range: "7d" });
  });

  it("parses a full query", () => {
    expect(parseFilters(qs("q=foo&method=request&status=err&range=7d"))).toEqual({
      search: "foo",
      method: "request",
      status: "err",
      period: { kind: "preset", range: "7d" },
    });
  });

  it.each([
    ["method=DELETE", "method", ""],
    ["method=", "method", ""],
    ["status=banana", "status", "all"],
    ["status=", "status", "all"],
  ])("junk %s falls back per-field", (query, field, want) => {
    expect(parseFilters(qs(query))[field as "method" | "status"]).toBe(want);
  });

  it("keeps valid fields when a neighbour is junk", () => {
    const f = parseFilters(qs("status=banana&q=foo&range=99h"));
    expect(f).toEqual({ search: "foo", method: "", status: "all", period: defaultPeriod });
  });

  it("parses a custom period", () => {
    expect(parseFilters(qs("from=2026-07-01T00:00:00Z&to=2026-07-02T00:00:00Z")).period).toEqual(
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
    expect(parseFilters(qs(query)).period).toEqual(defaultPeriod);
  });

  it("prefers range over from/to when both are present", () => {
    const f = parseFilters(qs("range=3h&from=2026-07-01T00:00:00Z&to=2026-07-02T00:00:00Z"));
    expect(f.period).toEqual({ kind: "preset", range: "3h" });
  });
});

describe("serializeFilters", () => {
  it("writes nothing when everything is default", () => {
    expect(serializeFilters(defaultFilters()).toString()).toBe("");
  });

  it("omits the period when it equals the starred default (§44.B)", () => {
    localStorage.setItem("nexus.overview.period", JSON.stringify({ kind: "preset", range: "7d" }));
    const f: OverviewFilters = { ...defaultFilters(), period: { kind: "preset", range: "7d" } };
    expect(serializeFilters(f).has("range")).toBe(false);
  });

  it("writes a non-default preset as range", () => {
    const f: OverviewFilters = { ...defaultFilters(), period: { kind: "preset", range: "1h" } };
    expect(serializeFilters(f).toString()).toBe("range=1h");
  });

  it("writes a custom period as from/to without range", () => {
    const f: OverviewFilters = {
      ...defaultFilters(),
      period: custom("2026-07-01T00:00:00Z", "2026-07-02T00:00:00Z"),
    };
    const out = serializeFilters(f);
    expect(out.get("from")).toBe("2026-07-01T00:00:00Z");
    expect(out.get("to")).toBe("2026-07-02T00:00:00Z");
    expect(out.has("range")).toBe(false);
  });

  it.each([
    [{ search: "foo" }, "q=foo"],
    [{ method: "requestAsync" as const }, "method=requestAsync"],
    [{ status: "degraded" as const }, "status=degraded"],
  ])("writes only the non-default field %o", (patch, want) => {
    expect(serializeFilters({ ...defaultFilters(), ...patch }).toString()).toBe(want);
  });
});

describe("round-trip", () => {
  it.each([
    ["all defaults", defaultFilters()],
    ["search only", { ...defaultFilters(), search: "da" }],
    [
      "everything set",
      {
        search: "da",
        method: "RabbitMQAsync" as const,
        status: "err" as const,
        period: { kind: "preset", range: "1h" } as Period,
      },
    ],
    [
      "custom period",
      { ...defaultFilters(), period: custom("2026-07-01T10:30:00Z", "2026-07-02T11:45:00Z") },
    ],
    ["search with spaces and plus", { ...defaultFilters(), search: "a b+c" }],
  ])("parse(serialize(f)) === f — %s", (_name, f) => {
    expect(parseFilters(serializeFilters(f as OverviewFilters))).toEqual(f);
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
    const out = applyFilters(prev, { ...defaultFilters(), search: "new" });
    expect(out.get("utm")).toBe("1");
    expect(out.get("q")).toBe("new");
    expect(out.has("status")).toBe(false); // вернулся к дефолту → ключ удалён
  });

  it("clears its own params when filters are default", () => {
    const out = applyFilters(qs("utm=1&q=old&range=1h"), defaultFilters());
    expect(out.toString()).toBe("utm=1");
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
    saveFilters(f);
    expect(loadFilters()).toEqual(f);
  });

  it("removes the key when everything is default (§54.4: cleared means nothing to restore)", () => {
    saveFilters({ ...defaultFilters(), search: "da" });
    expect(sessionStorage.getItem("nexus.overview.filters")).not.toBeNull();

    saveFilters(defaultFilters());
    expect(sessionStorage.getItem("nexus.overview.filters")).toBeNull();
    expect(loadFilters()).toBeNull();
  });

  it("returns null on an empty mirror", () => {
    expect(loadFilters()).toBeNull();
  });

  it.each([
    ["garbage==&&", null],
    ["status=banana&method=DELETE", null], // всё junk → всё-дефолт → нечего восстанавливать
  ])("tolerates junk in the mirror: %s", (raw, want) => {
    sessionStorage.setItem("nexus.overview.filters", raw);
    expect(loadFilters()).toBe(want);
  });

  it("sanitizes partially-junk mirror content", () => {
    sessionStorage.setItem("nexus.overview.filters", "q=foo&status=banana");
    expect(loadFilters()).toEqual({ ...defaultFilters(), search: "foo" });
  });
});

describe("storage failures (private mode)", () => {
  afterEach(() => vi.restoreAllMocks());

  it("saveFilters does not throw when the storage throws", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    expect(() => saveFilters({ ...defaultFilters(), search: "da" })).not.toThrow();
  });

  it("loadFilters returns null when the storage throws", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("SecurityError");
    });
    expect(loadFilters()).toBeNull();
  });
});
