import { describe, expect, it } from "vitest";

import {
  filterEntries,
  formatEntryLine,
  hasDistinctReplicas,
  levelBadgeClass,
  levelFromInt,
  levelToInt,
  logsQueryKey,
  serviceParam,
  stableStringify,
  type ServiceLogEntry,
} from "./logsUtils";

const entry = (over: Partial<ServiceLogEntry>): ServiceLogEntry => ({
  ts: "2026-07-15T12:00:00.123Z",
  level: "info",
  service: "web",
  msg: "hello",
  ...over,
});

describe("levelFromInt / levelToInt", () => {
  it("maps backend scale 2..5", () => {
    expect(levelFromInt(2)).toBe("error");
    expect(levelFromInt(3)).toBe("warn");
    expect(levelFromInt(4)).toBe("info");
    expect(levelFromInt(5)).toBe("debug");
  });

  it("falls back to info for out-of-range and missing", () => {
    expect(levelFromInt(0)).toBe("info");
    expect(levelFromInt(6)).toBe("info");
    expect(levelFromInt(undefined)).toBe("info");
    expect(levelFromInt(null)).toBe("info");
  });

  it("round-trips every level", () => {
    for (const l of ["error", "warn", "info", "debug"] as const) {
      expect(levelFromInt(levelToInt(l))).toBe(l);
    }
  });
});

describe("levelBadgeClass", () => {
  it("uses semantic tokens per level", () => {
    expect(levelBadgeClass("error")).toBe("text-err");
    expect(levelBadgeClass("warn")).toBe("text-warn");
    expect(levelBadgeClass("debug")).toBe("text-fg-subtle");
    expect(levelBadgeClass("info")).toBe("text-fg-muted");
    expect(levelBadgeClass("weird")).toBe("text-fg-muted");
  });
});

describe("filterEntries", () => {
  const entries = [
    entry({ service: "receiver", msg: "route resolved" }),
    entry({ service: "sender", msg: "redirect followed", attrs: { node: "site-push" } }),
    entry({ service: "web", msg: "settings updated" }),
  ];

  it("empty query keeps everything", () => {
    expect(filterEntries(entries, { query: "" })).toHaveLength(3);
    expect(filterEntries(entries, { query: "   " })).toHaveLength(3);
  });

  it("searches msg case-insensitively", () => {
    const got = filterEntries(entries, { query: "REDIRECT" });
    expect(got).toHaveLength(1);
    expect(got[0].msg).toBe("redirect followed");
  });

  it("searches inside attrs", () => {
    const got = filterEntries(entries, { query: "site-push" });
    expect(got).toHaveLength(1);
    expect(got[0].service).toBe("sender");
  });

  it("no match → empty", () => {
    expect(filterEntries(entries, { query: "nothing-here" })).toHaveLength(0);
  });
});

describe("serviceParam", () => {
  // Фильтр сервисов серверный: клиентский отсекал бы строки уже ПОСЛЕ того,
  // как болтливый сервис выбрал весь limit при мерже (поймано на стенде).
  it("empty or all three → undefined (server merges everything)", () => {
    expect(serviceParam([])).toBeUndefined();
    expect(serviceParam(["receiver", "sender", "web"])).toBeUndefined();
  });

  it("subset → csv in stable SERVICES order", () => {
    expect(serviceParam(["sender"])).toBe("sender");
    expect(serviceParam(["web", "receiver"])).toBe("receiver,web");
    expect(serviceParam(["sender", "receiver"])).toBe("receiver,sender");
  });

  it("order of clicks does not change the param", () => {
    expect(serviceParam(["web", "receiver"])).toBe(serviceParam(["receiver", "web"]));
  });
});

describe("stableStringify / formatEntryLine", () => {
  it("serializes attrs deterministically regardless of key order", () => {
    expect(stableStringify({ b: 1, a: { d: 2, c: 3 } })).toBe(stableStringify({ a: { c: 3, d: 2 }, b: 1 }));
    expect(stableStringify({ b: 1, a: 2 })).toBe('{"a":2,"b":1}');
  });

  it("formats a full line with upper-cased level and attrs", () => {
    const line = formatEntryLine(entry({ level: "warn", attrs: { node: "n1", status: 301 } }));
    expect(line).toContain("WARN");
    expect(line).toContain("[web]");
    expect(line).toContain("hello");
    expect(line).toContain('{"node":"n1","status":301}');
  });

  it("omits attrs block when empty", () => {
    expect(formatEntryLine(entry({}))).not.toContain("{");
  });
});

describe("logsQueryKey", () => {
  it("is stable for equal inputs (no volatile parts)", () => {
    expect(logsQueryKey([], 200)).toEqual(logsQueryKey([], 200));
    expect(JSON.stringify(logsQueryKey(["sender"], 500))).toBe(
      JSON.stringify(logsQueryKey(["sender"], 500)),
    );
  });

  it("differs by limit and by service selection", () => {
    expect(logsQueryKey([], 200)).not.toEqual(logsQueryKey([], 500));
    expect(logsQueryKey([], 200)).not.toEqual(logsQueryKey(["sender"], 200));
    expect(logsQueryKey([], 200)[0]).toBe("service-logs");
    expect(logsQueryKey([], 200)[1]).toBe("all");
  });

  it("is insensitive to chip click order (stable cache key)", () => {
    expect(logsQueryKey(["web", "receiver"], 200)).toEqual(logsQueryKey(["receiver", "web"], 200));
  });
});

describe("hasDistinctReplicas (§93.6)", () => {
  it("false when replica repeats the service name (single-instance install)", () => {
    // hostname контейнера в одиночной установке равен имени сервиса — колонка
    // с репликой дублировала бы соседнюю и показываться не должна.
    expect(
      hasDistinctReplicas([entry({ service: "web", replica: "web" }), entry({ service: "receiver", replica: "receiver" })]),
    ).toBe(false);
  });

  it("false when replica is absent (records written before §93)", () => {
    expect(hasDistinctReplicas([entry({}), entry({ replica: "" })])).toBe(false);
  });

  it("true as soon as one record comes from a named replica", () => {
    expect(
      hasDistinctReplicas([entry({ service: "web", replica: "web" }), entry({ service: "web", replica: "web-2" })]),
    ).toBe(true);
  });
});

describe("filterEntries by replica (§93.6)", () => {
  const entries = [
    entry({ msg: "first", service: "web", replica: "web-1" }),
    entry({ msg: "second", service: "web", replica: "web-2" }),
  ];

  it("finds rows of one replica by its name", () => {
    const got = filterEntries(entries, { query: "web-2" });
    expect(got).toHaveLength(1);
    expect(got[0]?.msg).toBe("second");
  });

  it("keeps matching by message intact", () => {
    expect(filterEntries(entries, { query: "first" })).toHaveLength(1);
  });
});
