import { forwardRef, type InputHTMLAttributes } from "react";

import { Input } from "./form";

type SearchInputProps = InputHTMLAttributes<HTMLInputElement> & {
  mono?: boolean;
};

// SearchInput — поле поиска или фильтра (§90.3).
//
// Существует ровно ради того, чтобы браузер не считал его полем формы и не
// предлагал автозаполнение. Одного `autocomplete="off"` мало: Chrome его
// намеренно игнорирует для полей, которые счёл контактными, а тип поля он
// определяет эвристикой по подсказке и подписи. Фильтр участников с текстом
// «имя, логин или email…» распознавался как поле email — поверх списка
// разворачивался попап с сохранёнными адресами.
//
// Что здесь собрано (каждый приём закрывает своего автозаполнителя):
//   - type="search" — семантика «это поиск, а не поле формы»; для search-полей
//     Chrome адресный автозаполнитель не предлагает;
//   - autocomplete="off" — базовое указание, его уважают Firefox и Safari;
//   - name="q" — нейтральное имя: эвристика Chrome смотрит и на него, а поля
//     без имени опознаются только по подсказке (то есть по слову «email»);
//   - data-1p-ignore / data-lpignore / data-form-type — сторонние менеджеры
//     паролей (1Password, LastPass, Dashlane), у которых своя эвристика и
//     собственные всплывающие подсказки.
//
// Нативный крестик очистки у type="search" скрыт в globals.css: он появляется
// поверх наших иконок и ломает вид поля.
//
// Полям, у которых Escape занят приложением, тип нужно вернуть текстовым
// (`type="text"`): у search-полей Chrome по Escape очищает значение сам, и это
// перебивает наши обработчики — например «Esc возвращает применённый фильтр»
// в журнале логов (§77.3). Остальная защита при этом сохраняется.
export const SearchInput = forwardRef<HTMLInputElement, SearchInputProps>(
  function SearchInput({ ...rest }, ref) {
    return (
      <Input
        ref={ref}
        type="search"
        name="q"
        autoComplete="off"
        data-1p-ignore
        data-lpignore="true"
        data-form-type="other"
        {...rest}
      />
    );
  },
);
