// logsQuery — ЕДИНСТВЕННЫЙ источник query-параметров фильтра логов узла (§72.2).
//
// До §72.2 параметры собирались в трёх местах и по-разному: список логов
// (useInfiniteQuery) слал всё, кроме status/done; счётчик «из M» (/logs/count) —
// включая status/done; SSE-стрим — своим URLSearchParams вообще без них. Из-за
// этого список фильтровался в браузере по уже загруженной странице, а счётчик —
// на сервере по всей таблице: «Показано 3 из 33», и скролл не догружал
// остальные 30 ошибок. Теперь набор параметров один на все три запроса, и
// разъехаться они не могут.

export type LogsStatusFilter = "all" | "ok" | "err";
export type LogsDoneFilter = "all" | "done" | "pending";

// LogsFilterState — полное состояние фильтра логов: расширенные фильтры (§48/§67)
// плюс быстрые переключатели «Все|ОК|Ошибки» и «Все|Завершено|В работе».
export type LogsFilterState = {
  q: string;
  qCase: boolean;
  qWord: boolean;
  qRegex: boolean;
  method: string;
  clientHost: string;
  // from/to — значения полей даты в формате <input type="datetime-local">
  // (локальная зона); в параметры уходят как ISO-строки.
  from: string;
  to: string;
  status: LogsStatusFilter;
  done: LogsDoneFilter;
};

// isoOrNull — datetime-local → ISO. Незаполненное или неразбираемое значение
// даёт null, и параметр не отправляется: `new Date("").toISOString()` бросает
// RangeError и уронил бы рендер вкладки на полу-введённой дате.
function isoOrNull(v: string): string | null {
  if (!v) return null;
  const ms = new Date(v).getTime();
  return Number.isNaN(ms) ? null : new Date(ms).toISOString();
}

// logsFilterParams — параметры фильтра для GET /logs и GET /logs/count.
// Пагинация (limit / to+before_id курсора) сюда НЕ входит: она своя у списка,
// а счётчик считает по фильтрам, а не по странице.
export function logsFilterParams(f: LogsFilterState): Record<string, string> {
  const p: Record<string, string> = {};
  if (f.q) {
    p.q = f.q;
    if (f.qCase) p.q_case = "1";
    if (f.qWord) p.q_word = "1";
    if (f.qRegex) p.q_regex = "1";
  }
  if (f.method) p.method = f.method;
  if (f.clientHost) p.client_host = f.clientHost; // §67
  const from = isoOrNull(f.from);
  if (from) p.from = from;
  const to = isoOrNull(f.to);
  if (to) p.to = to;
  // §72.2: быстрые фильтры уходят на сервер — иначе список и счётчик считают
  // разные множества, а скролл догружает записи, которые тут же отбрасываются.
  if (f.status !== "all") p.status = f.status;
  if (f.done !== "all") p.done = f.done === "done" ? "yes" : "no";
  return p;
}

// logsFilterSearch — те же параметры строкой для URL SSE-стрима
// (EventSource принимает готовый URL, а не params-объект). Пустой фильтр → "".
export function logsFilterSearch(f: LogsFilterState): string {
  const qs = new URLSearchParams(logsFilterParams(f)).toString();
  return qs ? `?${qs}` : "";
}

// isLogOK — «доставлено успешно»: запись закрыта и внешний узел ответил 2xx/3xx.
// Зеркало серверного предиката status=ok (§72.1: condLogOK в SQL, logRecordOK в
// live-tail). Используется для подсветки строки, чтобы «красная» строка и
// строка под фильтром «Ошибки» означали ровно одно и то же.
export function isLogOK(r: { done: boolean; status: number }): boolean {
  return r.done && r.status >= 200 && r.status < 400;
}
