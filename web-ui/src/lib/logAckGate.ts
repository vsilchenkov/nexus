import { type Node } from "../api/client";

/**
 * shouldShowAck — показывать ли блок пересчёта ответа шины (§84.9.1).
 *
 * Условие: у узла ВКЛЮЧЕНО изменение ответа И запись прошла async-путём.
 *
 * Второе — по ЗАПИСИ, а не по текущему типу узла, и это не перестраховка: по
 * §83.5 спека переживает перевод узла requestAsync → request и продолжает
 * работать, пока узел на паузе (§3.6). Значит у sync-узла могут лежать записи,
 * чей ответ шаблон РЕАЛЬНО формировал, а у бывшего async-узла — записи, к
 * которым он уже неприменим. Гейт по root_method прятал бы первые и показывал
 * вторые.
 *
 * Живёт в lib, а не рядом с компонентом: экспорт функции из файла компонента
 * валит react-refresh/only-export-components, а CI гоняет eslint с
 * --max-warnings=0.
 */
export function shouldShowAck(
  node: Pick<Node, "async_ack_spec">,
  recordType?: string,
): boolean {
  return !!node.async_ack_spec && recordType === "requestAsync";
}
