import { describe, expect, it } from "vitest";

import { buildVerifyMessage } from "./chTableVerify";
import type { CHTableVerifyResult } from "../api/client";

// t-заглушка: возвращает ключ и подставленные значения, чтобы тест проверял
// выбор ветки и содержимое перечислений, не завися от текста локали.
const t = (key: string, opts?: Record<string, unknown>) =>
  opts ? `${key}:${Object.values(opts).join("|")}` : key;

const ok: CHTableVerifyResult = {
  ok: true,
  table: "db.t",
  table_missing: false,
  missing: null,
  mismatched: null,
};

describe("buildVerifyMessage", () => {
  it("проверка не запускалась — сообщения нет", () => {
    expect(buildVerifyMessage(null, undefined, t)).toBeNull();
  });

  it("успех", () => {
    expect(buildVerifyMessage(null, ok, t)).toEqual({ ok: true, text: "node.verify.ok" });
  });

  it("таблицы нет", () => {
    const res = { ...ok, ok: false, table_missing: true };
    expect(buildVerifyMessage(null, res, t)).toEqual({ ok: false, text: "node.verify.table_missing" });
  });

  it("перечисляет отсутствующие колонки", () => {
    const res = { ...ok, ok: false, missing: ["node_id", "request_size"] };
    expect(buildVerifyMessage(null, res, t)).toEqual({
      ok: false,
      text: "node.verify.missing_columns:node_id, request_size",
    });
  });

  it("перечисляет расхождения типов как got → want", () => {
    const res = { ...ok, ok: false, mismatched: [{ name: "status", want: "Int32", got: "String" }] };
    expect(buildVerifyMessage(null, res, t)).toEqual({
      ok: false,
      text: "node.verify.type_mismatch:status: String → Int32",
    });
  });

  it("показывает оба вида расхождений сразу", () => {
    const res = {
      ...ok,
      ok: false,
      missing: ["node_id"],
      mismatched: [{ name: "done", want: "UInt8", got: "Int8" }],
    };
    const msg = buildVerifyMessage(null, res, t);
    expect(msg?.ok).toBe(false);
    expect(msg?.text).toContain("node.verify.missing_columns:node_id");
    expect(msg?.text).toContain("node.verify.type_mismatch:done: Int8 → UInt8");
  });

  it("ошибка вызова важнее прежнего результата — проверка не состоялась", () => {
    expect(buildVerifyMessage("ClickHouse недоступен", ok, t)).toEqual({
      ok: false,
      text: "ClickHouse недоступен",
    });
  });
});
