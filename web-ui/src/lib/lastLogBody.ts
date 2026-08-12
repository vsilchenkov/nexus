import { api } from "../api/client";

// §83.6: «Взять последний запрос из логов» — подстановка реального тела узла в
// образец для предпросмотра ack-шаблона.
//
// Вынесено из NodeSettings отдельным модулем по двум причинам, и обе — из
// разбора боевого дефекта:
//
//  1. ФОРМА ВЫЗОВА. Обёртка объявлена как `api.get(url, params)` — второй
//     аргумент И ЕСТЬ карта query-параметров. Здесь же передавали
//     `{ params: { which: "request" } }`, и axios сериализовал это как
//     `?params[which]=request`, то есть обязательный `which` до сервера не
//     доезжал вовсе и `/body` отвечал 400. Типизация промолчала: сигнатура
//     `params?: Record<string, unknown>` принимает `{ params: … }` как
//     совершенно законное значение. Ловится только тестом, который смотрит на
//     ФАКТИЧЕСКИЕ аргументы вызова.
//  2. РАЗЛИЧИМОСТЬ ПРИЧИН. Любой отказ (включая тот самый 400) показывался
//     одним текстом «записей нет или логирование тела выключено» — сообщение
//     уводило разбор в настройки узла, где всё было в порядке. Теперь причина
//     — замкнутое множество, и каждая имеет свой текст.
//
// Эталон формы вызова — LogsTab (`{ which, offset, limit }` плоско).

// sampleBodyLimitRunes — сколько рун тела тянем в образец. Это именно ОБРАЗЕЦ
// для проверки шаблона, а не просмотр журнала: подставлять мегабайты в поле
// формы незачем, а лимит источника подстановок всё равно 1 МиБ (§83.2).
const sampleBodyLimitRunes = 8192;

// LastLogBodyFailure — почему тело не удалось взять. Замкнутое множество:
// каждый вариант имеет собственный текст в интерфейсе, иначе разбор снова
// уедет не туда.
export type LastLogBodyFailure =
  // узел ещё не сохранён — журнала у него не существует
  | "no_node"
  // журнал доступен, но записей в нём нет
  | "no_records"
  // запись есть, а тело пустое: у узла выключено логирование тела либо
  // сохранённая копия пуста (multipart §68, нулевое тело)
  | "body_not_logged"
  // запрос к серверу не выполнен: сеть, 4xx, 5xx
  | "request_failed";

export type LastLogBodyResult = { ok: true; body: string } | { ok: false; reason: LastLogBodyFailure };

/**
 * fetchLastLogBody — тело последнего запроса узла из журнала.
 *
 * Тело в журнале ЗАМАСКИРОВАНО (§22.2) и может быть усечено `max_body_size` —
 * об этом предупреждает подсказка поля, здесь это не ошибка.
 */
export async function fetchLastLogBody(nodeId: string | undefined): Promise<LastLogBodyResult> {
  if (!nodeId) return { ok: false, reason: "no_node" };

  let logId: string | undefined;
  try {
    const list = await api.get<{ items?: Array<{ id: string }> }>(`/api/nodes/${nodeId}/logs`, {
      limit: 1,
    });
    logId = list.items?.[0]?.id;
  } catch {
    return { ok: false, reason: "request_failed" };
  }
  if (!logId) return { ok: false, reason: "no_records" };

  try {
    // Контракт /body: which обязателен (без него 400), chunk — само тело.
    const r = await api.get<{ chunk?: string }>(`/api/nodes/${nodeId}/log/${logId}/body`, {
      which: "request",
      offset: 0,
      limit: sampleBodyLimitRunes,
    });
    const body = r.chunk ?? "";
    if (body.trim() === "") return { ok: false, reason: "body_not_logged" };
    return { ok: true, body };
  } catch {
    return { ok: false, reason: "request_failed" };
  }
}
