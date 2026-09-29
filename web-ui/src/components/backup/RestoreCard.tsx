import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Ban, CheckCircle2, Upload } from "lucide-react";

import { api } from "../../api/client";
import { Button, Card, ErrorAlert, Field, Hint, Input, SectionHead, buttonClasses } from "../ui";
import { ConfirmRestoreDialog } from "./ConfirmRestoreDialog";
import { ImportPreview } from "./ImportPreview";
import {
  APPLY_TIMEOUT_MS,
  PREVIEW_TIMEOUT_MS,
  backupErrorText,
  type BackupStatus,
  type ImportOptions,
  type ImportPreview as Preview,
  type ImportResult,
} from "./types";

const DEFAULT_OPTIONS: ImportOptions = { status_mode: "pause_pull", adopt_unclaimed: false };

type Picked = { name: string; text: string };

// RestoreCard — восстановление из файла (§110.5–§110.9). На нечистом
// экземпляре выбор файла выключен с объяснением: сервер всё равно откажет,
// но администратор должен увидеть причину до того, как искать пароль.
export function RestoreCard({ status }: { status: BackupStatus }) {
  const { t } = useTranslation();
  const [result, setResult] = useState<ImportResult | null>(null);

  return (
    <Card>
      <SectionHead icon={<Upload className="h-4 w-4" />}>{t("settings.backup.restore.title")}</SectionHead>
      {result ? (
        <RestoreResult result={result} />
      ) : !status.clean ? (
        <Blocked text={t("settings.backup.restore.not_clean", { nodes: status.nodes, teams: status.teams })} />
      ) : !status.clickhouse_available ? (
        <Blocked text={t("settings.backup.restore.no_clickhouse")} />
      ) : (
        <RestoreFlow status={status} onDone={setResult} />
      )}
    </Card>
  );
}

function Blocked({ text }: { text: string }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-3">
      <Hint tone="danger" icon={<Ban className="h-3.5 w-3.5" />}>
        {text}
      </Hint>
      <Button disabled>{t("settings.backup.restore.choose_file")}</Button>
    </div>
  );
}

function RestoreFlow({ status, onDone }: { status: BackupStatus; onDone: (r: ImportResult) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const [picked, setPicked] = useState<Picked | null>(null);
  const [pass, setPass] = useState("");
  const [options, setOptions] = useState<ImportOptions>(DEFAULT_OPTIONS);
  const [confirming, setConfirming] = useState(false);

  const preview = useMutation({
    mutationFn: (opts: ImportOptions) =>
      api.post<Preview>(
        "/api/backup/import/preview",
        { file: picked?.text, passphrase: pass, options: opts },
        { timeout: PREVIEW_TIMEOUT_MS },
      ),
  });

  const apply = useMutation({
    mutationFn: (host: string) =>
      api.post<ImportResult>(
        "/api/backup/import/apply",
        {
          file: picked?.text,
          passphrase: pass,
          options,
          preview_digest: preview.data?.digest,
          confirm_host: host,
        },
        { timeout: APPLY_TIMEOUT_MS },
      ),
    onSuccess: (r) => {
      setConfirming(false);
      onDone(r);
      // Восстановлено всё: команды, узлы, справочники, настройки — любой
      // закешированный список устарел.
      qc.invalidateQueries();
    },
  });

  function reset() {
    preview.reset();
    apply.reset();
    setOptions(DEFAULT_OPTIONS);
  }

  async function onPick(f: File | undefined) {
    reset();
    // Без сброса повторный выбор того же файла после «Отмены» не дал бы
    // события change.
    if (!f && fileRef.current) fileRef.current.value = "";
    setPicked(f ? { name: f.name, text: await f.text() } : null);
  }

  function changeOptions(o: ImportOptions) {
    setOptions(o);
    preview.mutate(o);
  }

  // План, показанный администратору, обязан соответствовать текущим опциям:
  // применение сверяет дайджест предпросмотра, а он включает опции.
  const plan = preview.data;
  const planCurrent = !!plan && !preview.isPending && preview.variables === options;
  const canApply = planCurrent && plan.blockers.length === 0;

  return (
    <div className="space-y-4">
      <Hint tone="info" icon={<CheckCircle2 className="h-3.5 w-3.5" />}>
        {t("settings.backup.restore.clean", { nodes: status.nodes, teams: status.teams })}
      </Hint>

      <div className="grid max-w-xl grid-cols-1 gap-3 md:grid-cols-2">
        <Field label={t("settings.backup.restore.file")}>
          <div className="flex items-center gap-2">
            <Button sm onClick={() => fileRef.current?.click()}>
              {t("settings.backup.restore.choose_file")}
            </Button>
            <span className="min-w-0 truncate font-mono text-xs text-fg-muted">
              {picked?.name ?? t("settings.backup.restore.no_file")}
            </span>
            <input
              ref={fileRef}
              type="file"
              accept=".json,application/json"
              aria-label={t("settings.backup.restore.file")}
              className="hidden"
              onChange={(e) => void onPick(e.target.files?.[0])}
            />
          </div>
        </Field>
        <Field label={t("settings.backup.restore.passphrase")}>
          <Input
            type="password"
            autoComplete="off"
            value={pass}
            onChange={(e) => {
              setPass(e.target.value);
              reset();
            }}
          />
        </Field>
      </div>

      {!plan && (
        <Button
          variant="primary"
          disabled={!picked || pass === "" || preview.isPending}
          onClick={() => preview.mutate(options)}
        >
          {preview.isPending ? t("settings.backup.restore.checking") : t("settings.backup.restore.check")}
        </Button>
      )}

      {preview.isError && <ErrorAlert>{backupErrorText(preview.error, t)}</ErrorAlert>}

      {plan && (
        <>
          <p className="text-xs text-fg-muted">{t("settings.backup.restore.dry_run_note")}</p>
          <ImportPreview
            preview={plan}
            options={options}
            onOptionsChange={changeOptions}
            disabled={preview.isPending}
          />
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => void onPick(undefined)}>
              {t("common.cancel")}
            </Button>
            <Button variant="danger" disabled={!canApply} onClick={() => setConfirming(true)}>
              {t("settings.backup.restore.apply")}
            </Button>
          </div>
        </>
      )}

      {confirming && plan && (
        <ConfirmRestoreDialog
          preview={plan}
          host={window.location.hostname}
          pending={apply.isPending}
          error={apply.isError ? backupErrorText(apply.error, t) : null}
          onConfirm={(host) => apply.mutate(host)}
          onClose={() => {
            setConfirming(false);
            apply.reset();
          }}
        />
      )}
    </div>
  );
}

function RestoreResult({ result }: { result: ImportResult }) {
  const { t } = useTranslation();
  const p = result.preview;
  const missing = p.warnings.filter((w) => w.code === "external_table_missing");
  return (
    <div className="space-y-3">
      <Hint tone="info" icon={<CheckCircle2 className="h-3.5 w-3.5" />}>
        {t("settings.backup.result.done", {
          seconds: Math.max(1, Math.round(result.duration_ms / 1000)),
          teams: (p.entities.teams?.create ?? 0) + (p.entities.teams?.existing ?? 0),
          nodes: p.entities.nodes?.create ?? 0,
          paused: p.node_statuses.paused,
          users: p.entities.users?.create ?? 0,
          created: result.tables_created,
          updated: result.tables_updated,
        })}
      </Hint>
      {missing.length > 0 && (
        <Hint tone="warn">
          {t("settings.backup.result.external_missing")}{" "}
          <span className="font-mono">{missing.map((w) => w.subject).join(", ")}</span>
        </Hint>
      )}
      <Link to="/" className={buttonClasses({ sm: true })}>
        {t("settings.backup.result.to_nodes")}
      </Link>
    </div>
  );
}
