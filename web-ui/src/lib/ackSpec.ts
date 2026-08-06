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

// ackSpecFromForm собирает спеку для сохранения узла.
//
// Выключенный переключатель даёт null — «отвечать как раньше». Черновик
// шаблона при этом НЕ сохраняется: на сервере лежит либо рабочая спека, либо
// ничего, иначе выключенный узел хранил бы невалидный текст, который однажды
// «оживёт» при включении.
export function ackSpecFromForm(f: AckFormFields): AckSpec | null {
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
