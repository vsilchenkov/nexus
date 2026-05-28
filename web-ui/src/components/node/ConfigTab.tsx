import { useTranslation } from "react-i18next";
import { type ReactNode } from "react";

import { type Node } from "../../api/client";
import { Card, Chip } from "../ui";

// ConfigTab — вкладка «Конфигурация» узла (§21): read-only сводка настроек.
export function ConfigTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  return (
    <Card>
      <dl className="divide-y divide-line">
        <Row label={t("node.fields.method")}>
          <Chip>{node.root_method}</Chip>
        </Row>
        <Row label={t("node.fields.path")}>
          <span className="font-mono">{node.path}</span>
        </Row>
        <Row label={t("overview.table.status")}>{t(`node.status.${node.status}`)}</Row>
        <Row label={t("node.fields.url_mode")}>{node.url_mode}</Row>
        {node.url_mode === "static" && (
          <Row label={t("node.fields.target_url")}>
            <span className="font-mono break-all">{node.target_url || "—"}</span>
          </Row>
        )}
        <Row label={t("node.fields.auth")}>{node.auth_type || "—"}</Row>
        <Row label={t("node.fields.ch_table")}>
          <span className="font-mono">{node.clickhouse_table || "—"}</span>
        </Row>
        <Row label={t("common.updated_at")}>
          {new Date(node.updated_at).toLocaleString()}
        </Row>
      </dl>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[160px_1fr] gap-4 py-3 text-[13px]">
      <dt className="text-fg-muted">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
