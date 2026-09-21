import type { QueryClient } from "@tanstack/react-query";

import type { CatalogNode } from "../api/client";
import { containsFolded } from "./commentSnippet";
import { TEAM_SCOPE_ALL_PATH } from "./teamShare";

// Узлы, использующие элемент справочника (§104): диалог за счётчиком
// «узлов: N» в Настройках.

// CatalogKind — справочник, чьи узлы показывает диалог.
export type CatalogKind = "group" | "header" | "host";

const BASE: Record<CatalogKind, string> = {
  group: "/api/node-groups",
  header: "/api/headers",
  host: "/api/allowed-hosts",
};

// LIST_KEYS — ключи react-query, под которыми живут usage_count справочника:
// таблица Настроек и комбобокс формы узла.
const LIST_KEYS: Record<CatalogKind, readonly string[]> = {
  group: ["node-groups"],
  header: ["headers-catalog", "headers"],
  host: ["allowed-hosts", "allowed-hosts-search"],
};

// CATALOG_NODES_KEY — первый элемент ключа диалога.
export const CATALOG_NODES_KEY = "catalog-nodes";

// FILTER_THRESHOLD — поле фильтра появляется, только когда строк больше.
export const FILTER_THRESHOLD = 8;

export function catalogNodesUrl(kind: CatalogKind, id: string): string {
  return `${BASE[kind]}/${encodeURIComponent(id)}/nodes`;
}

export function catalogNodesKey(kind: CatalogKind, id: string) {
  return [CATALOG_NODES_KEY, kind, id] as const;
}

// invalidateCatalogList — перечитать usage_count справочника kind: число в
// таблице разошлось со списком узлов в диалоге (§104.6).
export function invalidateCatalogList(qc: QueryClient, kind: CatalogKind): void {
  for (const key of LIST_KEYS[kind]) void qc.invalidateQueries({ queryKey: [key] });
}

// invalidateCatalogUsage — узел изменился (привязка хоста, сохранение,
// удаление, копирование): счётчики всех справочников и открытые списки узлов
// устарели. Какой справочник задет, вызывающий обычно не знает — сохранение
// узла может сменить и группу, и заголовки разом.
export function invalidateCatalogUsage(qc: QueryClient): void {
  for (const kind of Object.keys(LIST_KEYS) as CatalogKind[]) invalidateCatalogList(qc, kind);
  void qc.invalidateQueries({ queryKey: [CATALOG_NODES_KEY] });
}

// filterCatalogNodes — фильтр по пути среди загруженных строк.
export function filterCatalogNodes(items: CatalogNode[], q: string): CatalogNode[] {
  const s = q.trim();
  return s ? items.filter((n) => containsFolded(n.path, s)) : items;
}

// groupDesktopHref — «Узлы» в сквозном режиме (§86) с фильтром по группе (§99.5).
export function groupDesktopHref(groupId: string): string {
  return `${TEAM_SCOPE_ALL_PATH}&group=${encodeURIComponent(groupId)}`;
}

// isHostBindingEffective — действует ли привязка хоста: Receiver проверяет
// allowlist только у узла с URL из запроса (§23). У остальных привязка ничего
// не разрешает, но блокирует удаление паттерна (§104.5).
export function isHostBindingEffective(n: Pick<CatalogNode, "url_mode">): boolean {
  return n.url_mode === "from_request";
}
