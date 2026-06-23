import { describe, it, expect } from "vitest";

import { prettyMaybe, formatRunes, PRETTY_MAX } from "./logBody";

describe("prettyMaybe", () => {
  it("pretty-prints small JSON", () => {
    expect(prettyMaybe('{"a":1}')).toBe('{\n  "a": 1\n}');
  });

  it("returns non-JSON as-is", () => {
    expect(prettyMaybe("not json <tag>")).toBe("not json <tag>");
  });

  it("does NOT parse bodies larger than PRETTY_MAX (no main-thread freeze)", () => {
    // Валидный, но огромный JSON-массив: выше порога возвращаем сырьём, без parse.
    const huge = "[" + "0,".repeat(PRETTY_MAX) + "0]";
    expect(huge.length).toBeGreaterThan(PRETTY_MAX);
    expect(prettyMaybe(huge)).toBe(huge);
  });
});

describe("formatRunes", () => {
  it.each([
    [0, "0"],
    [512, "512"],
    [65536, "64K"],
    [2_500_000, "2.4M"],
  ])("formats %i → %s", (n, want) => {
    expect(formatRunes(n)).toBe(want);
  });
});
