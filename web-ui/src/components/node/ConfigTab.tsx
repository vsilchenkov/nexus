import { useTranslation } from "react-i18next";
import { type ReactNode } from "react";

import { type Node } from "../../api/client";
import { useNodeUrlBuilder } from "../../lib/nodeUrl";
import { useMyTeams } from "../../lib/teams";
import { Card, Chip, CopyButton } from "../ui";

// ConfigTab — вкладка «Конфигурация» узла (§21): read-only сводка настроек.
export function ConfigTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  const isPull = node.root_method === "RabbitMQAsync";
  const verb = node.root_method === "request" ? "request" : "requestAsync";
  // §28 Пункт 1: адрес из публичного base URL приложения + slug команды.
  const buildUrl = useNodeUrlBuilder();
  const fullAddress = buildUrl(verb, node.path);
  // Команда узла: эндпоинт отдаёт только team_id, имя резолвим по членствам
  // (запрос общий с шапкой — react-query дедуплицирует ключ). Членства ещё не
  // пришли или команда не наша (у админа) → показываем сам идентификатор.
  const myTeams = useMyTeams();
  const team = myTeams.data?.items.find((tm) => tm.id === node.team_id);
  return (
    <Card>
      <dl className="divide-y divide-line">
        <Row label={t("node.fields.id")}>
          <div className="flex items-center gap-1.5">
            <span className="min-w-0 break-all font-mono text-[12px]">{node.id}</span>
            <CopyButton value={node.id} />
          </div>
        </Row>
        <Row label={t("node.fields.team")}>
          {team ? (
            <span>
              {team.name} <span className="font-mono text-fg-muted">({team.slug})</span>
            </span>
          ) : (
            <span className="font-mono text-[12px] break-all">{node.team_id || "—"}</span>
          )}
        </Row>
        <Row label={t("node.fields.method")}>
          <Chip>{node.root_method}</Chip>
        </Row>
        <Row label={t("node.fields.path")}>
          <span className="font-mono break-all">{node.path}</span>
        </Row>
        {!isPull && (
          <Row label={t("node.form.full_address")}>
            <div className="flex items-center gap-1.5">
              <span className="min-w-0 break-all font-mono text-[12px]">{fullAddress}</span>
              <CopyButton value={fullAddress} />
            </div>
          </Row>
        )}
        {!isPull && (
          <Row label={t("node.form.incoming_method")}>
            <Chip>{node.incoming_method || "POST"}</Chip>
          </Row>
        )}
        <Row label={t("node.form.outgoing_method")}>
          <Chip>{node.outgoing_method || "POST"}</Chip>
        </Row>
        <Row label={t("overview.table.status")}>{t(`node.status.${node.status}`)}</Row>
        <Row label={t("node.fields.url_mode")}>{node.url_mode}</Row>
        {node.url_mode === "static" && (
          <Row label={t("node.fields.target_url")}>
            <span className="font-mono break-all">{node.target_url || "—"}</span>
          </Row>
        )}
        <Row label={t("node.fields.auth")}>{node.auth_type || "—"}</Row>
        {/* Проброс заголовков — чипами, как в редакторе (HeadersField). Пустой
            список = наружу уходит только Content-Type (он пробрасывается всегда). */}
        <Row label={t("node.form.forward_headers")}>
          {node.forward_headers?.length ? (
            <div className="flex flex-wrap gap-1.5">
              {node.forward_headers.map((h) => (
                <Chip key={h}>
                  <span className="font-mono">{h}</span>
                </Chip>
              ))}
            </div>
          ) : (
            "—"
          )}
        </Row>
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
