// dryRunHint — какое поле авторизации ожидает узел от входящего запроса (§55.2).
//
// Тупик, ради которого §55 и затевался: диалог теста падал на шаге
// «Auth (incoming)» с `authorization header missing`, и оператор не знал, что
// подставлять. Логика зеркалит Receiver (`incomingAuthValue` в
// receiver/usecase/auth.go), включая его дефолты: пустой source = header,
// пустое имя поля = Authorization.

export type IncomingAuthHint = {
  // source — где узел ищет креду.
  source: "header" | "query";
  // field — имя заголовка или query-параметра.
  field: string;
  // example — что именно писать в значение (i18n-ключ подставит формат).
  scheme: "basic" | "token" | "signature";
};

type HintNode = {
  incoming_auth_type?: string;
  incoming_auth_dynamic_source?: string;
  incoming_auth_dynamic_field?: string;
  webhook_signature_header?: string;
};

// Методы, которые диалог теста умеет отправить (Select). ANY/пусто/неизвестное
// сводятся к POST.
const DRY_RUN_METHODS = ["POST", "GET", "PUT", "DELETE", "PATCH"];

// defaultDryRunMethod — метод по умолчанию для диалога теста: входящий метод узла
// (§40), чтобы узел с incoming_method=GET не падал на шаге method.incoming при
// дефолтном POST. Неподдерживаемое/ANY/пустое значение → POST.
export function defaultDryRunMethod(node: { incoming_method?: string } | null | undefined): string {
  const m = String(node?.incoming_method ?? "").toUpperCase();
  return DRY_RUN_METHODS.includes(m) ? m : "POST";
}

// incomingAuthHint возвращает null, если узел не требует авторизации: подсказка
// не нужна и не должна занимать место в диалоге.
export function incomingAuthHint(node: HintNode | null | undefined): IncomingAuthHint | null {
  const type = node?.incoming_auth_type ?? "none";
  if (type === "none" || type === "") return null;

  // webhook_signature — своё поле имени заголовка (§16), source всегда header:
  // подпись в query не принимается.
  if (type === "webhook_signature") {
    return {
      source: "header",
      field: node?.webhook_signature_header || "X-Signature",
      scheme: "signature",
    };
  }
  if (type !== "basic" && type !== "token") return null;

  // Дефолты Receiver'а: пустой source трактуется как header (back-compat для
  // узлов, сохранённых до миграции 0021), пустое имя поля — Authorization.
  const source = node?.incoming_auth_dynamic_source === "query" ? "query" : "header";
  return {
    source,
    field: node?.incoming_auth_dynamic_field || "Authorization",
    scheme: type,
  };
}
