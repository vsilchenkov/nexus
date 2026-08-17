import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SearchInput } from "./SearchInput";

// §90.3: жалоба «в фильтре участников браузер предлагает мои адреса». Одного
// autocomplete="off" не хватило — Chrome игнорирует его для полей, которые счёл
// контактными (подсказка фильтра содержит слово «email»). Тест держит весь
// набор признаков, по которым автозаполнители решают не вмешиваться.

describe("SearchInput (§90.3)", () => {
  it("несёт все признаки «это поиск, а не поле формы»", () => {
    render(<SearchInput placeholder="Фильтр участников: имя, логин или email…" />);
    const el = screen.getByPlaceholderText("Фильтр участников: имя, логин или email…");

    expect(el).toHaveAttribute("type", "search");
    expect(el).toHaveAttribute("autocomplete", "off");
    // Имя нейтральное: эвристика Chrome смотрит и на него, а без имени поле
    // опознаётся только по подсказке — то есть по слову «email».
    expect(el).toHaveAttribute("name", "q");
    // Сторонние менеджеры паролей со своей эвристикой и своими подсказками.
    expect(el).toHaveAttribute("data-1p-ignore");
    expect(el).toHaveAttribute("data-lpignore", "true");
    expect(el).toHaveAttribute("data-form-type", "other");
  });

  // Полям, где Escape занят приложением (поиск на Overview, фильтр логов §77.3),
  // тип возвращают текстовым: search-поле Chrome по Escape очищает сам.
  it("позволяет вернуть текстовый тип, сохраняя остальную защиту", () => {
    render(<SearchInput type="text" placeholder="поиск" />);
    const el = screen.getByPlaceholderText("поиск");

    expect(el).toHaveAttribute("type", "text");
    expect(el).toHaveAttribute("autocomplete", "off");
    expect(el).toHaveAttribute("data-1p-ignore");
  });
});
