import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Info } from "lucide-react";
import { Link } from "react-router-dom";

import { api, isNotFound, type CatalogNode, type CatalogNodesResp } from "../api/client";
import {
  catalogNodesKey,
  catalogNodesUrl,
  filterCatalogNodes,
  FILTER_THRESHOLD,
  groupDesktopHref,
  invalidateCatalogList,
  isHostBindingEffective,
  type CatalogKind,
} from "../lib/catalogUsage";
import { cn } from "../lib/cn";
import { ABOVE_STRETCH, STRETCH_HOST, STRETCHED_LINK } from "../lib/stretchedLink";
import { Button, buttonClasses, Chip, Modal, Pill, SearchInput } from "./ui";

type Props = {
  kind: CatalogKind;
  id: string;
  // name — имя элемента для заголовка (имя группы, заголовка, паттерн хоста).
  name: string;
  // usageCount — число из строки таблицы: при расхождении с total ответа
  // таблица справочника перечитывается (§104.6).
  usageCount: number;
  onClose: () => void;
};

// CatalogNodesDialog — узлы, использующие элемент справочника (§104.5): свои
// команды — списком ссылок, чужие — только числом. Только чтение: правка
// привязок остаётся в форме узла.
export function CatalogNodesDialog({ kind, id, name, usageCount, onClose }: Props) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [q, setQ] = useState("");

  // Список запрашивается при каждом открытии: таблица кеширует счётчик, и
  // именно диалог должен показать правду, если они разошлись.
  const nodesQ = useQuery({
    queryKey: catalogNodesKey(kind, id),
    queryFn: () => api.get<CatalogNodesResp>(catalogNodesUrl(kind, id)),
    staleTime: 0,
    refetchOnMount: "always",
  });

  // При ошибке перечитывания react-query хранит прежние data: элемент удалили
  // в другом окне — и под «элемент удалён» остался бы старый список.
  const data = nodesQ.error ? undefined : nodesQ.data;
  const notFound = isNotFound(nodesQ.error);
  const stale = notFound || (data !== undefined && data.total !== usageCount);
  useEffect(() => {
    if (stale) invalidateCatalogList(qc, kind);
  }, [stale, qc, kind]);

  return (
    <Modal
      title={t(`catalog_nodes.title.${kind}`, { name })}
      subtitle={data && t("catalog_nodes.summary", { total: data.total, visible: data.visible })}
      onClose={onClose}
      className="max-w-2xl"
      footer={
        <>
          {kind === "group" && data && data.visible > 0 && <GroupDesktopLink groupId={id} />}
          <Button variant="primary" onClick={onClose}>
            {t("common.close")}
          </Button>
        </>
      }
    >
      {nodesQ.isLoading && <div className="text-sm text-fg-muted">{t("common.loading")}</div>}
      {notFound && <div className="text-sm text-fg-muted">{t("catalog_nodes.deleted")}</div>}
      {nodesQ.error && !notFound && <div className="text-sm text-err">{t("common.error")}</div>}
      {data && <CatalogNodesBody kind={kind} data={data} q={q} onQ={setQ} />}
    </Modal>
  );
}

function CatalogNodesBody({
  kind,
  data,
  q,
  onQ,
}: {
  kind: CatalogKind;
  data: CatalogNodesResp;
  q: string;
  onQ: (q: string) => void;
}) {
  const { t } = useTranslation();
  if (data.total === 0) {
    return <div className="text-sm text-fg-muted">{t("catalog_nodes.unused")}</div>;
  }
  if (data.visible === 0) {
    return <HiddenNote text={t("catalog_nodes.all_hidden", { count: data.hidden })} />;
  }
  const rows = filterCatalogNodes(data.items, q);
  return (
    <div className="space-y-3">
      {data.items.length > FILTER_THRESHOLD && (
        <SearchInput
          className="max-w-xs"
          placeholder={t("catalog_nodes.filter")}
          value={q}
          onChange={(e) => onQ(e.target.value)}
        />
      )}
      {rows.length === 0 ? (
        <div className="text-sm text-fg-muted">{t("catalog_nodes.no_match")}</div>
      ) : (
        <ul className="space-y-1.5">
          {rows.map((n) => (
            <CatalogNodeRow key={n.id} kind={kind} node={n} />
          ))}
        </ul>
      )}
      {data.visible > data.items.length && (
        <div className="text-xs text-fg-muted">
          {t("catalog_nodes.truncated", { shown: data.items.length, total: data.visible })}
        </div>
      )}
      {data.hidden > 0 && <HiddenNote text={t("catalog_nodes.hidden", { count: data.hidden })} />}
    </div>
  );
}

const STATUS_TONE: Record<CatalogNode["status"], "ok" | "warn" | "err"> = {
  enabled: "ok",
  paused: "warn",
  disabled: "err",
};

// CatalogNodeRow — строка целиком НАСТОЯЩАЯ ссылка на узел (§100, урок §79):
// Ctrl+клик, средняя кнопка и «Открыть в новой вкладке» работают.
function CatalogNodeRow({ kind, node: n }: { kind: CatalogKind; node: CatalogNode }) {
  const { t } = useTranslation();
  const inert = kind === "host" && !isHostBindingEffective(n);
  return (
    <li className={cn(STRETCH_HOST, "rounded-md border border-line px-3 py-2 hover:bg-bg-muted")}>
      <div className="flex min-w-0 items-center gap-2">
        <Link
          to={`/nodes/${n.id}`}
          className={cn("min-w-0 truncate font-mono text-[13px] text-fg hover:text-accent", STRETCHED_LINK)}
        >
          {n.path}
        </Link>
        <Chip className="shrink-0">{n.root_method}</Chip>
        <Pill tone={STATUS_TONE[n.status] ?? "muted"} className="shrink-0">
          {t(`node.status.${n.status}`)}
        </Pill>
        <Chip tone="info" className="ml-auto shrink-0">
          {n.team_name}
        </Chip>
      </div>
      <div className="mt-0.5 flex min-w-0 items-center gap-2 text-[11px] text-fg-subtle">
        <span className="truncate">
          {n.url_mode === "from_request" ? t("catalog_nodes.url_from_request") : n.target_url}
        </span>
        {/* Поднят над ссылкой строки: иначе подсказка к бейджу не всплывёт. */}
        {inert && (
          <span className={cn(ABOVE_STRETCH, "shrink-0")} title={t("catalog_nodes.not_effective_tip")}>
            <Pill tone="warn">{t("catalog_nodes.not_effective")}</Pill>
          </span>
        )}
      </div>
    </li>
  );
}

// GroupDesktopLink — «Узлы» в сквозном режиме с фильтром по группе. Ссылка в
// облике кнопки, а не <Button>: действие — переход по адресу (§99.5, §86).
function GroupDesktopLink({ groupId }: { groupId: string }) {
  const { t } = useTranslation();
  return (
    <Link to={groupDesktopHref(groupId)} className={buttonClasses({ variant: "ghost" })}>
      {t("catalog_nodes.to_desktop")}
    </Link>
  );
}

function HiddenNote({ text }: { text: string }) {
  return (
    <div className="flex items-start gap-2 text-xs text-fg-muted">
      <Info className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden />
      <span>{text}</span>
    </div>
  );
}
