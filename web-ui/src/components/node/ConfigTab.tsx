import { useTranslation } from "react-i18next";
import { type ReactNode } from "react";

import { type Node } from "../../api/client";
import { useNodeUrlBuilder } from "../../lib/nodeUrl";
import { useNodeTeam } from "../../lib/nodeShare";
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
  // Команда узла — только человекочитаемое имя (без slug/UUID, §65): эндпоинт
  // узла отдаёт лишь team_id, имя резолвим по членствам (запрос общий с шапкой,
  // react-query дедуплицирует ключ) с фолбэком на резолвер §58 (тот уже
  // закеширован страницей узла через useEnsureNodeTeam). Имя недоступно → «—».
  const myTeams = useMyTeams();
  const nodeTeamQ = useNodeTeam(node.id);
  const teamName =
    myTeams.data?.items.find((tm) => tm.id === node.team_id)?.name ??
    nodeTeamQ.data?.team_name;
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
          <span>{teamName ?? "—"}</span>
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
        {/* Таймаут и ретраи одной строкой: «30000 ms · 2 × 1000 ms». Backoff в
            форме редактирования не выводится, но в API/домене есть — тут виден. */}
        <Row label={t("node.fields.timeout_retry")}>
          <span className="font-mono text-[12px]">
            {node.timeout_ms ?? "—"} ms
            {" · "}
            {t("node.fields.retries_fmt", {
              n: node.retry_count ?? 0,
              backoff: node.retry_backoff_ms ?? 0,
            })}
          </span>
        </Row>
        <Row label={t("overview.table.status")}>{t(`node.status.${node.status}`)}</Row>
        <Row label={t("node.fields.url_mode")}>{node.url_mode}</Row>
        {node.url_mode === "static" && (
          <Row label={t("node.fields.target_url")}>
            <div className="flex items-center gap-1.5">
              <span className="min-w-0 font-mono break-all">{node.target_url || "—"}</span>
              {node.target_url && <CopyButton value={node.target_url} />}
            </div>
          </Row>
        )}
        {/* Авторизация раздельно: входящая (клиент → Receiver; скрыта для pull —
            входящих HTTP-запросов нет) и исходящая (Sender → target). Тип none/
            пусто = строка не показывается. Для basic рядом логин (auth_login из
            API — не секрет; пароль наружу не отдаётся никогда). */}
        {!isPull && node.incoming_auth_type && node.incoming_auth_type !== "none" && (
          <Row label={t("node.fields.incoming_auth")}>
            <div className="flex flex-wrap items-center gap-1.5">
              <Chip>{node.incoming_auth_type}</Chip>
              {node.incoming_auth_login && (
                <span className="text-[12px]">
                  <span className="text-fg-muted">{t("node.fields.login_label")} </span>
                  <span className="font-mono">{node.incoming_auth_login}</span>
                </span>
              )}
            </div>
          </Row>
        )}
        {node.auth_type && node.auth_type !== "none" && (
          <Row label={t("node.fields.outgoing_auth")}>
            <div className="flex flex-wrap items-center gap-1.5">
              <Chip>{node.auth_type}</Chip>
              {node.auth_login && (
                <span className="text-[12px]">
                  <span className="text-fg-muted">{t("node.fields.login_label")} </span>
                  <span className="font-mono">{node.auth_login}</span>
                </span>
              )}
            </div>
          </Row>
        )}
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
        {/* §63: «Создано» — всегда, с автором создателя. «Обновлено» — только
            если узел меняли после создания (updated_at ≠ created_at; при
            создании оба таймстемпа равны одному now() транзакции). */}
        <Row label={t("common.created_at")}>
          <DateWithAuthor at={node.created_at} by={node.created_by} t={t} />
        </Row>
        {node.updated_at !== node.created_at && (
          <Row label={t("common.updated_at")}>
            <DateWithAuthor at={node.updated_at} by={node.updated_by} t={t} />
          </Row>
        )}
      </dl>
    </Card>
  );
}

// DateWithAuthor — дата + «Автор: <логин>» (§63). Логин моноширинный. Если автор
// не заполнен (узлы до миграции 0026) — показываем только дату, без «Автор: —»:
// пустая подпись не несёт информации и зашумляет строку.
function DateWithAuthor({
  at,
  by,
  t,
}: {
  at: string;
  by?: string;
  t: (k: string) => string;
}) {
  return (
    <span>
      {new Date(at).toLocaleString()}
      {by && (
        <span className="text-fg-subtle">
          {" · "}
          {t("common.author")}: <span className="font-mono">{by}</span>
        </span>
      )}
    </span>
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
