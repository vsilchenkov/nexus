import { useEffect } from "react";

import { useInstanceID } from "./instance";

// §89.2: код ноды в заголовке вкладки браузера — «Nexus KZ».
//
// У ноды без идентификатора заголовок прежний, «Nexus»: интерфейс одиночной
// установки не меняется вовсе — тот же принцип, что у бейджа в шапке (§70.8,
// Topbar).

// APP_TITLE — базовый заголовок. Литерал, а не i18n-ключ `app.title`: имя
// продукта не переводится (в ru.json и en.json там одна и та же строка), а
// привязка к языку означала бы перерисовку <title> при переключении локали без
// единой видимой причины. `app.title` остаётся тем, чем был, — заголовком на
// экранах входа и сброса пароля.
export const APP_TITLE = "Nexus";

// documentTitle — чистая функция «код ноды → заголовок». Вынесена отдельно,
// чтобы правило (пусто → без суффикса, иначе ЗАГЛАВНЫМИ) проверялось без DOM и
// без react-query.
export function documentTitle(instance: string): string {
  const id = instance.trim().toUpperCase();
  return id === "" ? APP_TITLE : `${APP_TITLE} ${id}`;
}

// useDocumentTitle — держит <title> вкладки в соответствии с кодом ноды.
//
// Монтируется РОВНО ОДИН РАЗ, в App: заголовок один на документ, и два писателя
// дрались бы за document.title (та же линия, что у TeamUrlSync — единственная
// точка монтирования зеркала §76).
//
// Сетевого запроса не прибавляет: данные берутся из уже существующего queryKey
// ["version"] (useInstanceID, staleTime Infinity) — того самого, который читают
// футер сайдбара и форма входа. react-query дедуплицирует наблюдателей ключа.
//
// Пока /api/version не ответил (или ответил ошибкой), instance пуст и в
// заголовке остаётся «Nexus» — ровно то, что уже написано в index.html, поэтому
// мигания при загрузке нет.
export function useDocumentTitle(): void {
  const instance = useInstanceID();
  useEffect(() => {
    document.title = documentTitle(instance);
  }, [instance]);
}
