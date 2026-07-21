import { describe, expect, it } from "vitest";

import { chSchemaChangeWontApply, chSyncFormDirty } from "./chSchema";

const saved = {
  clickhouse_template_id: "tpl-1",
  clickhouse_table: "db.logs",
  clickhouse_retention_days: 30,
  external_table: false,
};

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

describe("chSyncFormDirty", () => {
  const base = {
    isNew: false,
    currentTemplateId: "tpl-1",
    currentTable: "db.logs",
    currentRetentionDays: 30,
    currentExternalTable: false,
    saved,
  };

  it("грязно: изменён retention (это и есть кейс со стенда — 50 → 40)", () => {
    expect(chSyncFormDirty({ ...base, currentRetentionDays: 40 })).toBe(true);
  });

  it("грязно: изменён шаблон", () => {
    expect(chSyncFormDirty({ ...base, currentTemplateId: "tpl-2" })).toBe(true);
  });

  it("грязно: изменено имя таблицы", () => {
    expect(chSyncFormDirty({ ...base, currentTable: "db.logs_v2" })).toBe(true);
  });

  it("грязно: переключили «внешняя таблица» (§64 — решает, ведёт ли схему Nexus)", () => {
    expect(chSyncFormDirty({ ...base, currentExternalTable: true })).toBe(true);
  });

  it("чисто: CH-поля совпадают с сохранёнными", () => {
    expect(chSyncFormDirty(base)).toBe(false);
  });

  it("чисто: создание нового узла (сохранённого ещё нет)", () => {
    expect(chSyncFormDirty({ ...base, isNew: true, saved: null })).toBe(false);
  });
});
