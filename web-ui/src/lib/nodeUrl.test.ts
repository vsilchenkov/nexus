import { describe, expect, it } from "vitest";

import { buildLegacyNodeUrl, buildNodeUrl } from "./nodeUrl";

const base = "https://nexus.example.com";

describe("buildNodeUrl (§78.1, короткая форма)", () => {
  it("подставляет slug команды", () => {
    expect(buildNodeUrl({ publicBaseUrl: base, teamSlug: "webhook", path: "sbp-qr" })).toBe(
      "https://nexus.example.com/api/v1/webhook/sbp-qr",
    );
  });

  it("опускает slug у команды default", () => {
    expect(buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "sbp-qr" })).toBe(
      "https://nexus.example.com/api/v1/sbp-qr",
    );
  });

  it("у default показывает slug, если первый сегмент пути — сегмент-метод", () => {
    // Без слога такой адрес Receiver прочитал бы как legacy-форму и ушёл бы
    // искать узел «x» вместо «request/x».
    expect(buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "request/x" })).toBe(
      "https://nexus.example.com/api/v1/default/request/x",
    );
    expect(buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "callback/x" })).toBe(
      "https://nexus.example.com/api/v1/default/callback/x",
    );
  });

  it("регистр сегмента-метода значим — как в Receiver", () => {
    expect(
      buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "requestasync/x" }),
    ).toBe("https://nexus.example.com/api/v1/requestasync/x");
    expect(
      buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "requestAsync/x" }),
    ).toBe("https://nexus.example.com/api/v1/default/requestAsync/x");
  });

  it("похожий, но не совпадающий сегмент слога не требует", () => {
    expect(buildNodeUrl({ publicBaseUrl: base, teamSlug: "default", path: "requests/x" })).toBe(
      "https://nexus.example.com/api/v1/requests/x",
    );
  });

  it("срезает хвостовые слеши публичного адреса", () => {
    expect(buildNodeUrl({ publicBaseUrl: base + "///", teamSlug: "webhook", path: "a" })).toBe(
      "https://nexus.example.com/api/v1/webhook/a",
    );
  });

  it("без публичного адреса берёт origin браузера", () => {
    expect(buildNodeUrl({ teamSlug: "webhook", path: "a" })).toBe(
      `${window.location.origin}/api/v1/webhook/a`,
    );
  });
});

describe("buildLegacyNodeUrl (классическая форма)", () => {
  it("включает сегмент метода и slug", () => {
    expect(
      buildLegacyNodeUrl({
        publicBaseUrl: base,
        teamSlug: "webhook",
        verb: "requestAsync",
        path: "sbp-qr",
      }),
    ).toBe("https://nexus.example.com/api/v1/requestAsync/webhook/sbp-qr");
  });

  it("у команды default слог опускает — форма остаётся прежней", () => {
    expect(
      buildLegacyNodeUrl({
        publicBaseUrl: base,
        teamSlug: "default",
        verb: "request",
        path: "sbp-qr",
      }),
    ).toBe("https://nexus.example.com/api/v1/request/sbp-qr");
  });
});
