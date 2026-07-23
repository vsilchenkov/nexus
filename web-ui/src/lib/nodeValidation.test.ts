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
    auth_login: "",
    auth_password: "",
    incoming_auth_login: "",
    incoming_auth_password: "",
    timeout_ms: 30000,
    retry_count: 0,
    retry_backoff_ms: 1000,
    dlq_ttl_seconds: 86400,
    dlq_retry_delay_seconds: 300,
    logging_enabled: true,
    max_body_size_enabled: false,
    max_body_size: 0,
    clickhouse_table: "",
    clickhouse_template_id: "",
    external_table: false,
    rmq_host: "",
    rmq_queue: "",
    pull_interval_sec: 5,
    pull_batch_size: 100,
    pull_prefetch: 100,
  };
}

describe("validateNodeForm — §64 внешняя таблица", () => {
  it("внешняя таблица несовместима с шаблоном", () => {
    const v = validateNodeForm({ ...base(), external_table: true, clickhouse_template_id: "tpl-1" });
    expect(v?.field).toBe("clickhouse_template_id");
    expect(v?.code).toBe("node.validation.external_table_conflict");
  });

  it("внешняя таблица без шаблона — ok", () => {
    expect(validateNodeForm({ ...base(), external_table: true })).toBeNull();
  });

  it("шаблон без внешней таблицы — ok", () => {
    expect(validateNodeForm({ ...base(), clickhouse_template_id: "tpl-1" })).toBeNull();
  });
});

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

describe("validateNodeForm — basic-креды Логин/Пароль", () => {
  const ctx = { orig_auth_login: "olduser", orig_incoming_auth_login: "" };

  it("двоеточие в логине запрещено (исходящая)", () => {
    const v = validateNodeForm({ ...base(), auth_type: "basic", auth_login: "a:b" });
    expect(v?.field).toBe("auth_login");
    expect(v?.code).toBe("node.validation.login_colon");
  });

  it("двоеточие в логине запрещено (входящая)", () => {
    const v = validateNodeForm({ ...base(), incoming_auth_type: "basic", incoming_auth_login: "x:y" });
    expect(v?.field).toBe("incoming_auth_login");
  });

  it("смена логина без пароля → ошибка (пароль сервер не отдаёт)", () => {
    const v = validateNodeForm(
      { ...base(), auth_type: "basic", auth_login: "newuser", auth_password: "" },
      ctx,
    );
    expect(v?.field).toBe("auth_password");
    expect(v?.code).toBe("node.validation.password_required_on_login_change");
  });

  it("смена логина с паролем — ок", () => {
    expect(
      validateNodeForm(
        { ...base(), auth_type: "basic", auth_login: "newuser", auth_password: "pw" },
        ctx,
      ),
    ).toBeNull();
  });

  it("логин не менялся, пароль пуст — ок (оставить старые креды)", () => {
    expect(
      validateNodeForm(
        { ...base(), auth_type: "basic", auth_login: "olduser", auth_password: "" },
        ctx,
      ),
    ).toBeNull();
  });

  it("новый узел: логин без пароля → ошибка", () => {
    const v = validateNodeForm(
      { ...base(), incoming_auth_type: "basic", incoming_auth_login: "user", incoming_auth_password: "" },
      ctx,
    );
    expect(v?.field).toBe("incoming_auth_password");
  });

  it("без ctx (нет данных об оригинале) правило смены логина не применяется", () => {
    expect(
      validateNodeForm({ ...base(), auth_type: "basic", auth_login: "user", auth_password: "" }),
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

  // Симптом со стенда: логирование выключено, поле осталось с дефолтным
  // префиксом «nexus_x.» — сохранение не должно блокироваться (поля карточки
  // задизейблены, исправить их нельзя, не включив логи).
  it("логирование выключено → черновик имени не проверяется", () => {
    expect(
      validateNodeForm({ ...base(), logging_enabled: false, clickhouse_table: "nexussend." }),
    ).toBeNull();
  });

  it("логирование выключено → нулевой лимит тела при включённом чекбоксе ок", () => {
    expect(
      validateNodeForm({
        ...base(),
        logging_enabled: false,
        clickhouse_table: "nexus_default.x",
        max_body_size_enabled: true,
        max_body_size: 0,
      }),
    ).toBeNull();
  });

  it("логирование включено → нулевой лимит тела при включённом чекбоксе — ошибка", () => {
    const v = validateNodeForm({
      ...base(),
      clickhouse_table: "nexus_default.x",
      max_body_size_enabled: true,
      max_body_size: 0,
    });
    expect(v?.code).toBe("node.validation.max_body_size_required");
  });
});
