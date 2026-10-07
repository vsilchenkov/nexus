import type { TFunction } from "i18next";

// Типы раздела «Резервная копия» (§110) — зеркало JSON-тегов
// internal/web/adapter/in/http/backup_handler.go и usecase/backup_import*.go.

export type BackupStatus = {
  nodes: number;
  teams: number;
  users: number;
  api_tokens: number;
  // clean — нет узлов и команд сверх Default: единственное состояние, в
  // котором сервер примет восстановление.
  clean: boolean;
  clickhouse_available: boolean;
  instance_id: string;
  nexus_version: string;
  schema_version: number;
  min_passphrase_len: number;
};

export type StatusMode = "pause_pull" | "as_file" | "pause_all";

export type ImportOptions = {
  status_mode: StatusMode;
  adopt_unclaimed: boolean;
};

export type BackupIssue = { code: string; subject?: string; detail?: string };

export type EntityPlan = { create: number; existing: number; replace?: number };

export type CHDatabasePlan = {
  team: string;
  name: string;
  ownership: "none" | "owned" | "unclaimed" | "foreign" | "conflict" | "unknown";
  tables: number;
  action: "create" | "reuse" | "claim" | "adopt" | "blocked";
};

export type ImportPreview = {
  header: {
    exported_at: string;
    nexus_version: string;
    schema_version: number;
    source_instance_id: string;
    counts: Record<string, number>;
  };
  exported_by: string;
  source_public_url: string;
  entities: Record<string, EntityPlan>;
  skipped: BackupIssue[];
  databases: CHDatabasePlan[];
  tables: {
    create: number;
    existing: number;
    external: number;
    external_missing: number;
    unmanaged: number;
  };
  node_statuses: { enabled: number; paused: number; disabled: number };
  settings_sections: string[];
  warnings: BackupIssue[];
  blockers: BackupIssue[];
  digest: string;
};

export type ImportResult = {
  preview: ImportPreview;
  databases_claimed: number;
  tables_created: number;
  tables_updated: number;
  duration_ms: number;
};

// ENTITY_ORDER — порядок сущностей в предпросмотре: сначала то, ради чего
// восстанавливают (команды, узлы, люди), потом справочники.
export const ENTITY_ORDER = [
  "teams",
  "nodes",
  "users",
  "api_tokens",
  "node_groups",
  "hosts",
  "headers",
  "request_fields",
  "ch_templates",
  "peer_instances",
] as const;

// APPLY_TIMEOUT_MS — потолок ожидания применения на клиенте: сервер сам
// ограничивает восстановление 10 минутами, клиент ждёт чуть дольше, чтобы
// увидеть настоящий ответ, а не свой таймаут.
export const APPLY_TIMEOUT_MS = 11 * 60_000;

// PREVIEW_TIMEOUT_MS — предпросмотр выводит ключ из пароля (Argon2id) и
// прогоняет всю запись в PostgreSQL с откатом.
export const PREVIEW_TIMEOUT_MS = 2 * 60_000;

// hasAdoptCandidate — в ClickHouse есть непустая база без маркера владения:
// только тогда имеет смысл галочка «занять существующие базы».
export function hasAdoptCandidate(p: ImportPreview): boolean {
  return p.databases.some((d) => d.ownership === "unclaimed" && d.tables > 0);
}

// backupErrorText — текст отказа сервера. Перевод по code в языке интерфейса
// (сервер переводит по Accept-Language, он может отличаться); detail —
// техническая причина (какие строки файла не сошлись, какой DDL упал).
export function backupErrorText(e: unknown, t: TFunction): string {
  const d = (e as { response?: { data?: { error?: string; code?: string; detail?: string } } })
    ?.response?.data;
  // passphrase_too_short несёт число в тексте сервера — его перевод без числа
  // был бы хуже, поэтому берётся текст как есть.
  const base =
    d?.code && d.code !== "backup.passphrase_too_short" ? t(d.code) : (d?.error ?? t("common.error"));
  return d?.detail ? `${base}: ${d.detail}` : base;
}
