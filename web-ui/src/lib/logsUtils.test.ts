import { describe, expect, it } from "vitest";

import {
  filterEntries,
  formatEntryLine,
  levelBadgeClass,
  levelFromInt,
  levelToInt,
  logsQueryKey,
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

  it("empty services = all", () => {
    expect(filterEntries(entries, { services: [], query: "" })).toHaveLength(3);
  });

  it("filters by service chips", () => {
    const got = filterEntries(entries, { services: ["sender"], query: "" });
    expect(got).toHaveLength(1);
    expect(got[0].service).toBe("sender");
  });

  it("searches msg case-insensitively", () => {
    const got = filterEntries(entries, { services: [], query: "REDIRECT" });
    expect(got).toHaveLength(1);
    expect(got[0].msg).toBe("redirect followed");
  });

  it("searches inside attrs", () => {
    const got = filterEntries(entries, { services: [], query: "site-push" });
    expect(got).toHaveLength(1);
    expect(got[0].service).toBe("sender");
  });

  it("combines service and query", () => {
    expect(filterEntries(entries, { services: ["web"], query: "redirect" })).toHaveLength(0);
    expect(filterEntries(entries, { services: ["sender"], query: "redirect" })).toHaveLength(1);
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
    expect(logsQueryKey(200)).toEqual(logsQueryKey(200));
    expect(JSON.stringify(logsQueryKey(500))).toBe(JSON.stringify(logsQueryKey(500)));
  });

  it("differs only by limit", () => {
    expect(logsQueryKey(200)).not.toEqual(logsQueryKey(500));
    expect(logsQueryKey(200)[0]).toBe("service-logs");
  });
});
