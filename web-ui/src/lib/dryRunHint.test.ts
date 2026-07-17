import { describe, expect, it } from "vitest";

import { incomingAuthHint } from "./dryRunHint";

describe("incomingAuthHint", () => {
  it.each([
    ["none", { incoming_auth_type: "none" }],
    ["пусто", { incoming_auth_type: "" }],
    ["поле отсутствует", {}],
    ["узла нет", null],
  ])("не подсказывает, когда авторизация не нужна: %s", (_name, node) => {
    expect(incomingAuthHint(node)).toBeNull();
  });

  // Дефолты Receiver'а: узлы, сохранённые до миграции 0021, приходят с пустым
  // source/field — подсказка обязана показать то же, что реально проверяется.
  it("пустые source/field = header + Authorization", () => {
    expect(incomingAuthHint({ incoming_auth_type: "token" })).toEqual({
      source: "header",
      field: "Authorization",
      scheme: "token",
    });
  });

  it("явный header с нестандартным именем", () => {
    expect(
      incomingAuthHint({
        incoming_auth_type: "basic",
        incoming_auth_dynamic_source: "header",
        incoming_auth_dynamic_field: "X-Auth",
      }),
    ).toEqual({ source: "header", field: "X-Auth", scheme: "basic" });
  });

  // §41: креда может ожидаться в query — ровно тот случай, когда оператор шлёт
  // заголовок и получает «missing», не понимая почему.
  it("query как источник", () => {
    expect(
      incomingAuthHint({
        incoming_auth_type: "token",
        incoming_auth_dynamic_source: "query",
        incoming_auth_dynamic_field: "access_token",
      }),
    ).toEqual({ source: "query", field: "access_token", scheme: "token" });
  });

  it("webhook_signature: имя из своего поля, источник всегда header", () => {
    expect(
      incomingAuthHint({
        incoming_auth_type: "webhook_signature",
        webhook_signature_header: "X-Hub-Signature-256",
        // Подпись в query не принимается — source должен игнорироваться.
        incoming_auth_dynamic_source: "query",
      }),
    ).toEqual({ source: "header", field: "X-Hub-Signature-256", scheme: "signature" });
  });

  it("webhook_signature без имени заголовка — разумный дефолт", () => {
    expect(incomingAuthHint({ incoming_auth_type: "webhook_signature" })?.field).toBe("X-Signature");
  });

  it("неизвестный тип не роняет диалог", () => {
    expect(incomingAuthHint({ incoming_auth_type: "future_mode" })).toBeNull();
  });
});
