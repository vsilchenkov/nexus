import { describe, expect, it } from "vitest";

import { chSchemaChangeWontApply } from "./chSchema";

const saved = { clickhouse_template_id: "tpl-1", clickhouse_table: "db.logs" };

describe("chSchemaChangeWontApply", () => {
  it("предупреждает: сменили шаблон, имя таблицы то же", () => {
    expect(
      chSchemaChangeWontApply({
        isNew: false,
        loggingEnabled: true,
        currentTemplateId: "tpl-2",
        currentTable: "db.logs",
        saved,
      }),
    ).toBe(true);
  });

  it("не предупреждает: сменили и имя таблицы (создастся новая — схема применится)", () => {
    expect(
      chSchemaChangeWontApply({
        isNew: false,
        loggingEnabled: true,
        currentTemplateId: "tpl-2",
        currentTable: "db.logs_v2",
        saved,
      }),
    ).toBe(false);
  });

  it("не предупреждает: шаблон не менялся", () => {
    expect(
      chSchemaChangeWontApply({
        isNew: false,
        loggingEnabled: true,
        currentTemplateId: "tpl-1",
        currentTable: "db.logs",
        saved,
      }),
    ).toBe(false);
  });

  it("не предупреждает: создание нового узла (таблицы ещё нет)", () => {
    expect(
      chSchemaChangeWontApply({
        isNew: true,
        loggingEnabled: true,
        currentTemplateId: "tpl-2",
        currentTable: "db.logs",
        saved: null,
      }),
    ).toBe(false);
  });

  it("не предупреждает: логирование выключено (таблица не пишется)", () => {
    expect(
      chSchemaChangeWontApply({
        isNew: false,
        loggingEnabled: false,
        currentTemplateId: "tpl-2",
        currentTable: "db.logs",
        saved,
      }),
    ).toBe(false);
  });
});
