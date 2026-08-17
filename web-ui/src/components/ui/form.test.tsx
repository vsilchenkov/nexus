import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Input } from "./form";

// §90.3: в админ-панели автозаполнение по умолчанию выключено. Жалоба, из-за
// которой это появилось: на поле «Фильтр участников: имя, логин или email»
// браузер разворачивал попап менеджера паролей — он перекрывал список
// участников и предлагал вписать в фильтр чужой логин.
//
// Тест держит именно ДЕФОЛТ атома: без него достаточно одного нового поля без
// атрибута, чтобы вернуть прежнее поведение.

describe("Input — автозаполнение (§90.3)", () => {
  it("по умолчанию выключает автозаполнение", () => {
    render(<Input placeholder="фильтр" />);
    expect(screen.getByPlaceholderText("фильтр")).toHaveAttribute("autocomplete", "off");
  });

  // Формы входа и смены пароля обязаны переопределять дефолт: там подстановка
  // сохранённого пароля — правильное поведение.
  it("не мешает формам авторизации задать своё значение", () => {
    render(
      <>
        <Input placeholder="логин" autoComplete="username" />
        <Input placeholder="пароль" type="password" autoComplete="current-password" />
      </>,
    );
    expect(screen.getByPlaceholderText("логин")).toHaveAttribute("autocomplete", "username");
    expect(screen.getByPlaceholderText("пароль")).toHaveAttribute(
      "autocomplete",
      "current-password",
    );
  });
});
