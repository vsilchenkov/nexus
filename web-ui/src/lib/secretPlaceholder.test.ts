import { describe, expect, it } from "vitest";

import { SECRET_KEEP_KEY, SECRET_SET_KEY, secretPlaceholderKey } from "./secretPlaceholder";

// §98.1: правило подсказки пустого поля секрета. Проверяется таблицей, потому
// что случаев четыре, а в форме узла правило повторяется пять раз — расхождение
// между полями заметить глазами нельзя.
describe("secretPlaceholderKey", () => {
  const rows: {
    name: string;
    in: { isNew: boolean; isSet: boolean | undefined; typeUnchanged: boolean };
    want: string | null;
  }[] = [
    {
      name: "создание узла — подсказки этого модуля нет вовсе",
      in: { isNew: true, isSet: false, typeUnchanged: true },
      want: null,
    },
    {
      name: "создание узла: флаг из ниоткуда не делает поле «установленным»",
      in: { isNew: true, isSet: true, typeUnchanged: true },
      want: null,
    },
    {
      name: "правка, секрет задан, тип авторизации прежний — «Установлен»",
      in: { isNew: false, isSet: true, typeUnchanged: true },
      want: SECRET_SET_KEY,
    },
    {
      name: "правка, секрета нет — прежняя подсказка «не менять»",
      in: { isNew: false, isSet: false, typeUnchanged: true },
      want: SECRET_KEEP_KEY,
    },
    {
      name: "правка, флага нет вовсе (старая сборка Web) — прежняя подсказка",
      in: { isNew: false, isSet: undefined, typeUnchanged: true },
      want: SECRET_KEEP_KEY,
    },
    {
      name: "правка, секрет задан, но тип авторизации переключили — «Установлен» врал бы",
      in: { isNew: false, isSet: true, typeUnchanged: false },
      want: SECRET_KEEP_KEY,
    },
  ];

  for (const r of rows) {
    it(r.name, () => {
      expect(secretPlaceholderKey(r.in)).toBe(r.want);
    });
  }

  // Ключи обязаны совпадать с locales/*.json: опечатка здесь даёт не ошибку
  // сборки, а сырой ключ в интерфейсе.
  it("ключи совпадают с ключами локалей", () => {
    expect(SECRET_SET_KEY).toBe("node.form.secret_set");
    expect(SECRET_KEEP_KEY).toBe("node.form.keep_secret");
  });
});
