import { render, screen } from "@testing-library/react";
import { createRef } from "react";
import { describe, expect, it } from "vitest";

import { Command, CommandInput } from "./Command";

// §90.3: жалоба «в выборе участника команды браузер предлагает мои адреса».
// Поле поиска внутри cmdk-попапа несло только имя и метки менеджеров паролей —
// Chrome всё равно считал его контактным (в подсказке слово «email») и
// разворачивал поверх списка попап автозаполнения. Недостающий признак — тип
// search; передать его пропсом нельзя (cmdk дописывает свой type="text" после
// спреда), поэтому он выставляется через ref. Тест держит и набор признаков, и
// то, что ref вызывающей стороны при этом не потерялся.

describe("CommandInput (§90.3)", () => {
  const placeholder = "Поиск: имя, логин или email…";

  it("несёт все признаки «это поиск, а не поле формы»", () => {
    render(
      <Command>
        <CommandInput placeholder={placeholder} />
      </Command>,
    );
    const el = screen.getByPlaceholderText(placeholder);

    expect(el).toHaveAttribute("type", "search");
    expect(el).toHaveAttribute("autocomplete", "off");
    expect(el).toHaveAttribute("name", "q");
    expect(el).toHaveAttribute("data-1p-ignore");
    expect(el).toHaveAttribute("data-lpignore", "true");
    expect(el).toHaveAttribute("data-form-type", "other");
  });

  // Полю, которое само обрабатывает Escape, тип возвращают текстовым:
  // search-поле Chrome по Escape очищает сам (§77.3).
  it("позволяет вернуть текстовый тип, сохраняя остальную защиту", () => {
    render(
      <Command>
        <CommandInput type="text" placeholder={placeholder} />
      </Command>,
    );
    const el = screen.getByPlaceholderText(placeholder);

    expect(el).toHaveAttribute("type", "text");
    expect(el).toHaveAttribute("data-1p-ignore");
  });

  it("пробрасывает ref вызывающей стороны", () => {
    const ref = createRef<HTMLInputElement>();
    render(
      <Command>
        <CommandInput ref={ref} placeholder={placeholder} />
      </Command>,
    );

    expect(ref.current).toBe(screen.getByPlaceholderText(placeholder));
  });
});
