import { describe, it, expect } from "vitest";

import { validateNodeForm, type NodeFormLimits } from "./nodeValidation";

function base(): NodeFormLimits {
  return {
    path: "svc/x",
    root_method: "request",
    url_mode: "static",
    target_url: "https://example.com",
    url_param_name: "url_base",
    auth_type: "none",
    auth_dynamic_field: "",
    incoming_auth_type: "none",
    incoming_auth_dynamic_source: "header",
    incoming_auth_dynamic_field: "Authorization",
    timeout_ms: 30000,
    retry_count: 0,
    retry_backoff_ms: 1000,
    dlq_ttl_seconds: 86400,
    dlq_retry_delay_seconds: 300,
    max_body_size_enabled: false,
    max_body_size: 0,
    clickhouse_table: "",
    rmq_host: "",
    rmq_queue: "",
    pull_interval_sec: 5,
    pull_batch_size: 100,
    pull_prefetch: 100,
  };
}

describe("validateNodeForm — §41 обязательность поля динамической авторизации", () => {
  it("ok по умолчанию (без динамической авторизации)", () => {
    expect(validateNodeForm(base())).toBeNull();
  });

  it("token_from_request требует поле", () => {
    const v = validateNodeForm({ ...base(), auth_type: "token_from_request", auth_dynamic_field: "" });
    expect(v?.field).toBe("auth_dynamic_field");
    expect(v?.code).toBe("node.validation.auth_field_required");
  });

  it("token_from_request ok с выбранным полем", () => {
    expect(validateNodeForm({ ...base(), auth_type: "token_from_request", auth_dynamic_field: "Bearer" })).toBeNull();
  });

  it("basic_from_request требует поле", () => {
    const v = validateNodeForm({ ...base(), auth_type: "basic_from_request", auth_dynamic_field: "" });
    expect(v?.field).toBe("auth_dynamic_field");
  });

  it("входящий token + source=query требует поле", () => {
    const v = validateNodeForm({
      ...base(),
      incoming_auth_type: "token",
      incoming_auth_dynamic_source: "query",
      incoming_auth_dynamic_field: "",
    });
    expect(v?.field).toBe("incoming_auth_dynamic_field");
  });

  it("входящий token + source=header ok без явного выбора (дефолт Authorization)", () => {
    expect(
      validateNodeForm({
        ...base(),
        incoming_auth_type: "token",
        incoming_auth_dynamic_source: "header",
        incoming_auth_dynamic_field: "Authorization",
      }),
    ).toBeNull();
  });
});

describe("validateNodeForm — §42 формат имени CH-таблицы", () => {
  it("пустое имя ок (логирование опционально)", () => {
    expect(validateNodeForm({ ...base(), clickhouse_table: "" })).toBeNull();
  });

  it("корректное db.table ок", () => {
    expect(validateNodeForm({ ...base(), clickhouse_table: "nexus_default.my_table" })).toBeNull();
  });

  it.each([
    ["без БД", "my_table"],
    ["с пробелом", "db.my table"],
    ["спецсимвол", "db.my-table"],
    ["две точки", "db.schema.table"],
    ["точка в конце", "db."],
    ["SQL-инъекция", "nexus.x; DROP TABLE y--"],
  ])("невалидное имя (%s) → ошибка формата", (_name, table) => {
    const v = validateNodeForm({ ...base(), clickhouse_table: table });
    expect(v?.field).toBe("clickhouse_table");
    expect(v?.code).toBe("node.validation.clickhouse_table_format");
  });
});
