import { useTranslation } from "react-i18next";
import { AlertTriangle, Ban, Database, FileCheck2 } from "lucide-react";

import { Hint } from "../ui";
import {
  ENTITY_ORDER,
  hasAdoptCandidate,
  type BackupIssue,
  type ImportOptions,
  type ImportPreview as Preview,
  type StatusMode,
} from "./types";

const STATUS_MODES: StatusMode[] = ["pause_pull", "as_file", "pause_all"];

// ImportPreview — план восстановления (§110.8): сервер уже прогнал всю запись
// в PostgreSQL с откатом, здесь — что будет создано, заменено и пропущено,
// решения по ClickHouse, предупреждения и блокирующие проблемы. Опции
// меняют план, поэтому их смена перезапускает предпросмотр (onOptionsChange).
export function ImportPreview({
  preview,
  options,
  onOptionsChange,
  disabled,
}: {
  preview: Preview;
  options: ImportOptions;
  onOptionsChange: (o: ImportOptions) => void;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const h = preview.header;

  return (
    <div className="space-y-3 text-[13px]">
      <div className="flex items-start gap-2 text-fg-muted">
        <FileCheck2 className="mt-0.5 h-4 w-4 shrink-0" />
        <span>
          {t("settings.backup.preview.file_meta", {
            at: new Date(h.exported_at).toLocaleString(),
            version: h.nexus_version,
            instance: h.source_instance_id || t("settings.backup.preview.no_instance"),
            schema: h.schema_version,
          })}
          {preview.exported_by && ` · ${t("settings.backup.preview.exported_by", { user: preview.exported_by })}`}
        </span>
      </div>

      <Entities preview={preview} />
      <Replaced preview={preview} />
      <ClickHouse preview={preview} />

      <p>
        {t("settings.backup.preview.node_statuses", {
          enabled: preview.node_statuses.enabled,
          paused: preview.node_statuses.paused,
          disabled: preview.node_statuses.disabled,
        })}
      </p>

      <Issues title={t("settings.backup.preview.skipped")} issues={preview.skipped} tone="muted" />
      <Issues title={t("settings.backup.preview.warnings")} issues={preview.warnings} tone="warn" />
      <Issues title={t("settings.backup.preview.blockers")} issues={preview.blockers} tone="danger" />

      <fieldset className="space-y-1.5" disabled={disabled}>
        <legend className="mb-1 text-xs text-fg-muted">{t("settings.backup.preview.status_mode")}</legend>
        {STATUS_MODES.map((m) => (
          <label key={m} className="flex items-center gap-2">
            <input
              type="radio"
              name="backup-status-mode"
              checked={options.status_mode === m}
              onChange={() => onOptionsChange({ ...options, status_mode: m })}
            />
            {t(`settings.backup.status_mode.${m}`)}
          </label>
        ))}
      </fieldset>

      {hasAdoptCandidate(preview) && (
        <label className="flex items-start gap-2">
          <input
            type="checkbox"
            className="mt-0.5"
            disabled={disabled}
            checked={options.adopt_unclaimed}
            onChange={(e) => onOptionsChange({ ...options, adopt_unclaimed: e.target.checked })}
          />
          <span>
            {t("settings.backup.preview.adopt")}
            <span className="block text-xs text-fg-muted">{t("settings.backup.preview.adopt_hint")}</span>
          </span>
        </label>
      )}
    </div>
  );
}

function Entities({ preview }: { preview: Preview }) {
  const { t } = useTranslation();
  const parts = ENTITY_ORDER.flatMap((k) => {
    const e = preview.entities[k];
    if (!e || e.create + e.existing === 0) return [];
    const label = t(`settings.backup.entity.${k}`);
    return [
      e.existing > 0
        ? t("settings.backup.preview.entity_with_existing", { label, create: e.create, existing: e.existing })
        : t("settings.backup.preview.entity", { label, create: e.create }),
    ];
  });
  return (
    <p>
      <span className="font-medium">{t("settings.backup.preview.will_create")}</span> {parts.join(" · ")}
    </p>
  );
}

function Replaced({ preview }: { preview: Preview }) {
  const { t } = useTranslation();
  const masks = preview.entities.log_masks;
  const parts: string[] = [];
  if (masks) {
    parts.push(t("settings.backup.preview.replace_masks", { create: masks.create, replace: masks.replace ?? 0 }));
  }
  if (preview.settings_sections.length > 0) {
    parts.push(t("settings.backup.preview.replace_settings", { sections: preview.settings_sections.join(", ") }));
  }
  if (parts.length === 0) return null;
  return (
    <p>
      <span className="font-medium">{t("settings.backup.preview.will_replace")}</span> {parts.join(" · ")}
    </p>
  );
}

function ClickHouse({ preview }: { preview: Preview }) {
  const { t } = useTranslation();
  const count = (a: string) => preview.databases.filter((d) => d.action === a).length;
  const tb = preview.tables;
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1.5 font-medium">
        <Database className="h-3.5 w-3.5 text-fg-muted" /> ClickHouse
      </div>
      <p className="pl-5">
        {t("settings.backup.preview.databases", {
          create: count("create") + count("claim") + count("adopt"),
          reuse: count("reuse"),
        })}
      </p>
      <p className="pl-5">
        {t("settings.backup.preview.tables", { create: tb.create, existing: tb.existing, external: tb.external })}
      </p>
      {preview.databases.length > 0 && (
        <details className="pl-5 text-xs text-fg-muted">
          <summary className="cursor-pointer">{t("settings.backup.preview.databases_list")}</summary>
          <ul className="mt-1 space-y-0.5">
            {preview.databases.map((d) => (
              <li key={d.name}>
                <span className="font-mono">{d.name}</span> — {d.team} ·{" "}
                {t(`settings.backup.db_action.${d.action}`)}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}

// Issues — список проблем одного вида. Перевод кода подставляет subject
// (узел, база, поле) и, где это число или адрес, detail; техническая причина,
// которую перевод не использовал, дописывается после тире.
function Issues({ title, issues, tone }: { title: string; issues: BackupIssue[]; tone: "muted" | "warn" | "danger" }) {
  const { t } = useTranslation();
  if (issues.length === 0) return null;
  const icon = tone === "danger" ? <Ban className="h-3.5 w-3.5" /> : <AlertTriangle className="h-3.5 w-3.5" />;
  return (
    <Hint tone={tone} icon={icon}>
      <div className="font-medium">
        {title} ({issues.length})
      </div>
      <ul className="mt-1 space-y-0.5">
        {issues.map((i, n) => {
          const subject = i.subject ?? "";
          const detail = i.detail ?? "";
          const text = t(`settings.backup.issue.${i.code}`, {
            subject,
            detail,
            defaultValue: [i.code, subject].filter(Boolean).join(" "),
          });
          return (
            <li key={`${i.code}-${subject}-${n}`}>
              {text}
              {detail && !text.includes(detail) && <span className="ml-1 opacity-80">— {detail}</span>}
            </li>
          );
        })}
      </ul>
    </Hint>
  );
}
