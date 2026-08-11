import type { AckSpec } from "../api/client";

// §83: преобразование «плоские поля формы ↔ спека узла».
//
// В форме поля лежат плоско (контролы и подсветка ошибок работают как у
// остальных), а на сервер уходит вложенный объект. Логика вынесена из
// компонента: это чистое преобразование, и проверять его удобнее отдельно от
// страницы с десятком запросов.

export type AckFormFields = {
  ack_enabled: boolean;
  ack_content_type: "application/json" | "text/plain";
  ack_status: number;
  ack_on_error: "default" | "error";
  ack_body: string;
};

export const ackFormDefaults: AckFormFields = {
  ack_enabled: false,
  ack_content_type: "application/json",
  ack_status: 0,
  ack_on_error: "default",
  ack_body: "",
};

// AckRootMethod — тип узла в терминах формы: от него зависит, существует ли
// переопределение ответа вообще.
export type AckRootMethod = "request" | "requestAsync" | "RabbitMQAsync";

/**
 * ackSpecApplies — переопределение ответа существует ТОЛЬКО у `requestAsync`.
 *
 * Ответ «принято в очередь» есть только там, где очередь есть: у sync-узла
 * клиент получает ответ приёмника, у pull-узла (`RabbitMQAsync`) входящего
 * HTTP нет вовсе и отвечать некому. Показывать группу в этих двух случаях
 * значит предлагать настройку, которая ни на что не влияет.
 *
 * Одно правило на два места — показ группы и сборку payload'а: пока условие
 * было записано дважды, форма прятала карточку у pull-узлов, но продолжала бы
 * отправлять спеку, попади она в поля.
 */
export function ackSpecApplies(rootMethod: AckRootMethod): boolean {
  return rootMethod === "requestAsync";
}

// ackSpecFromForm собирает спеку для сохранения узла.
//
// Выключенный переключатель даёт null — «отвечать как раньше». Черновик
// шаблона при этом НЕ сохраняется: на сервере лежит либо рабочая спека, либо
// ничего, иначе выключенный узел хранил бы невалидный текст, который однажды
// «оживёт» при включении.
//
// rootMethod — обязательный аргумент, а не «если надо, проверьте снаружи»:
// сохранение узла как sync ВЫКЛЮЧАЕТ переопределение, и правило не должно
// зависеть от того, вспомнил ли о нём вызывающий.
export function ackSpecFromForm(f: AckFormFields, rootMethod: AckRootMethod): AckSpec | null {
  if (!ackSpecApplies(rootMethod)) return null;
  if (!f.ack_enabled) return null;
  return {
    version: 1,
    status: f.ack_status,
    content_type: f.ack_content_type,
    body: f.ack_body,
    on_error: f.ack_on_error,
  };
}

// ackFormFromSpec раскладывает спеку узла по полям формы. Отсутствие спеки —
// выключенный переключатель с дефолтами, а не пустая карточка.
export function ackFormFromSpec(spec: AckSpec | null | undefined): AckFormFields {
  if (!spec) return { ...ackFormDefaults };
  return {
    ack_enabled: true,
    ack_content_type: spec.content_type ?? ackFormDefaults.ack_content_type,
    ack_status: spec.status ?? 0,
    ack_on_error: spec.on_error ?? ackFormDefaults.ack_on_error,
    ack_body: spec.body ?? "",
  };
}
