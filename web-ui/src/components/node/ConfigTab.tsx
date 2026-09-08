import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode } from "react";

import { api, type Node, type NodeGroup } from "../../api/client";
import { useNodeUrlBuilder } from "../../lib/nodeUrl";
import { useNodeTeam } from "../../lib/nodeShare";
import { useNodeTeamName } from "../../lib/nodeTeamName";
import { useMyTeams } from "../../lib/teams";
import { Card, Chip, CopyButton } from "../ui";

// ConfigTab — вкладка «Конфигурация» узла (§21): read-only сводка настроек.
export function ConfigTab({ node }: { node: Node }) {
  const { t } = useTranslation();
  const isPull = node.root_method === "RabbitMQAsync";
  const verb = node.root_method === "request" ? "request" : "requestAsync";
  // Команда узла — только человекочитаемое имя (без slug/UUID, §65). Расчёт
  // общий с подсказкой кнопки «По умолчанию» на вкладках «Обзор»/«Очередь»
  // (§92.3), поэтому живёт в useNodeTeamName. Имя недоступно → «—».
  const myTeams = useMyTeams();
  const nodeTeamQ = useNodeTeam(node.id);
  const membership = myTeams.data?.items.find((tm) => tm.id === node.team_id);
  const teamName = useNodeTeamName(node);
  // §99: имя группы — из общего справочника (queryKey ["node-groups", ""], тот
  // же, что у комбобокса формы: один запрос на страницу, а не два).
  const groupsQ = useQuery({
    queryKey: ["node-groups", ""],
    queryFn: () => api.get<{ items: NodeGroup[] }>("/api/node-groups", { q: "", limit: 200 }),
    staleTime: 30_000,
    enabled: !!node.group_id,
  });
  const groupName = node.group_id
    ? groupsQ.data?.items.find((g) => g.id === node.group_id)?.name
    : undefined;
  // §28 Пункт 1: адрес из публичного base URL приложения + slug команды.
  // Команда берётся ОТ УЗЛА, а не из сессии (§89.6): в членствах вызывающего
  // команды узла может не быть вовсе, и резолвер §58 — единственный источник.
  const buildUrl = useNodeUrlBuilder({
    slug: nodeTeamQ.data?.team_slug ?? membership?.slug,
    externalUrl: nodeTeamQ.data?.team_external_url ?? membership?.external_url,
  });
  const address = buildUrl(verb, node.path);
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
        {/* §99.7: группа — сразу после команды. Справочник глобальный, поэтому
            имя резолвится без team-скоупа; группы нет или справочник ещё не
            приехал → «—», а не пустая строка. */}
        <Row label={t("node.fields.group")}>
          <span>{groupName ?? "—"}</span>
        </Row>
        <Row label={t("node.fields.method")}>
          <Chip>{node.root_method}</Chip>
        </Row>
        <Row label={t("node.fields.path")}>
          <span className="font-mono break-all">{node.path}</span>
        </Row>
        {!isPull && (
          <Row label={t("node.form.full_address")}>
            {/* §78.5: основной — короткий адрес (без request/requestAsync);
                классический показан ниже, он работает без ограничения срока. */}
            <div className="space-y-1">
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 break-all font-mono text-[12px]">{address.short}</span>
                <CopyButton value={address.short} />
              </div>
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 break-all font-mono text-[11px] text-fg-subtle">
                  {t("node.form.legacy_address")}: {address.legacy}
                </span>
                <CopyButton value={address.legacy} />
              </div>
              {/* §89.4: адрес узла снаружи контура — внешняя ссылка команды
                  плюс путь узла. Строки нет, пока ссылка у команды не задана:
                  пустой «внешняя: » сообщал бы, что адреса не существует, тогда
                  как его просто не настроили. */}
              {address.external && (
                <div className="flex items-center gap-1.5">
                  <span className="min-w-0 break-all font-mono text-[11px] text-fg-subtle">
                    {t("node.form.external_address")}: {address.external}
                  </span>
                  <CopyButton value={address.external} />
                </div>
              )}
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

// DateWithAuthor — дата, под ней «Автор: <логин>» отдельной строкой (§63; на
// одной строке не вмещалось в узких колонках). Логин моноширинный. Если автор
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
        <span className="block text-fg-subtle">
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
