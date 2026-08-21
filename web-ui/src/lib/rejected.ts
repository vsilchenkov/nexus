// Типы и справочники журнала отказов на входе (§94).
//
// Зеркало ответов /api/rejected*: имена полей совпадают с JSON бэкенда.

export type RejectedGroup = {
  id: string;
  team_slug: string;
  team_id?: string;
  team_name?: string;
  node_path: string;
  reason: string;
  http_method: string;
  status: number;
  first_seen: string;
  last_seen: string;
  count: number;
  clients: number;
  resolved_at?: string;
  resolved_by?: string;
};

export type RejectedListResp = {
  groups: RejectedGroup[];
  total: number;
  // retention_days и collecting отвечают на вопрос, который пустой список сам
  // не отвечает: «отказов не было» или «журнал выключен настройкой» (§94.5).
  retention_days: number;
  collecting: boolean;
};

export type RejectedClient = {
  ip: string;
  host?: string;
  user_agent?: string;
  count: number;
  first_seen: string;
  last_seen: string;
};

export type RejectedSample = {
  at: string;
  client_ip: string;
  http_method: string;
  raw_path: string;
  status: number;
  body_bytes: number;
  request_id?: string;
  headers: Record<string, string>;
};

export type RejectedSuggestion = {
  node_id: string;
  path: string;
  team_slug: string;
  distance: number;
};

export type RejectedDetails = {
  group: RejectedGroup;
  clients: RejectedClient[];
  samples: RejectedSample[];
  suggestion?: RejectedSuggestion;
};

export type RejectedSummary = {
  count: number;
  groups: number;
  clients: number;
  unresolved: number;
  retention_days: number;
  collecting: boolean;
};

// REJECT_REASONS — коды причин из §94.2 в порядке частоты на практике.
// Зеркало domain.RejectReason: значения уходят в query-параметр `reasons`, и
// расхождение молча сузило бы фильтр.
export const REJECT_REASONS = [
  "node_not_found",
  "method_not_allowed",
  "unauthorized",
  "node_disabled",
  "url_not_allowed",
  "rate_limited",
  "body_too_large",
  "loop_detected",
  "bad_request",
  "other",
] as const;

export type RejectReason = (typeof REJECT_REASONS)[number];

// reasonLabelKey — ключ i18n для причины. Неизвестный код (журнал новее
// интерфейса) показывается как есть, а не пустой строкой.
export function reasonLabelKey(reason: string): string {
  return (REJECT_REASONS as readonly string[]).includes(reason)
    ? `rejected.reason.${reason}`
    : "";
}
