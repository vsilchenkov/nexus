import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Download, ShieldAlert } from "lucide-react";

import { api } from "../../api/client";
import { Button, Card, ErrorAlert, Field, Hint, Input, SectionHead } from "../ui";
import { backupErrorText, saveBlob, type BackupStatus } from "./types";

// ExportCard — выгрузка всей конфигурации в зашифрованный паролем файл
// (§110.4). Пароль вводится дважды: опечатка в нём делает файл
// невосстановимым, а узнают об этом в момент аварии.
export function ExportCard({ status }: { status: BackupStatus }) {
  const { t } = useTranslation();
  const [pass, setPass] = useState("");
  const [repeat, setRepeat] = useState("");
  const [saved, setSaved] = useState<string | null>(null);

  const exp = useMutation({
    mutationFn: () => api.postBlob("/api/backup/export", { passphrase: pass }),
    onSuccess: ({ blob, filename }) => {
      const name = filename ?? "nexus-backup.json";
      saveBlob(blob, name);
      setSaved(name);
      setPass("");
      setRepeat("");
    },
  });

  const min = status.min_passphrase_len;
  const tooShort = [...pass].length < min;
  const mismatch = repeat !== "" && repeat !== pass;
  const canExport = !tooShort && repeat === pass && !exp.isPending;

  return (
    <Card>
      <SectionHead icon={<Download className="h-4 w-4" />}>{t("settings.backup.export.title")}</SectionHead>
      <p className="text-[13px] text-fg">
        {t("settings.backup.export.includes", {
          teams: status.teams,
          nodes: status.nodes,
          users: status.users,
          api_tokens: status.api_tokens,
        })}
      </p>
      <p className="mt-1 text-xs text-fg-muted">{t("settings.backup.export.excludes")}</p>

      <div className="mt-4 grid max-w-xl grid-cols-1 gap-3 md:grid-cols-2">
        <Field label={t("settings.backup.export.passphrase")} hint={t("settings.backup.export.passphrase_hint", { min })}>
          <Input
            type="password"
            autoComplete="new-password"
            value={pass}
            onChange={(e) => {
              setPass(e.target.value);
              setSaved(null);
            }}
          />
        </Field>
        <Field label={t("settings.backup.export.passphrase_repeat")}>
          <Input
            type="password"
            autoComplete="new-password"
            value={repeat}
            onChange={(e) => setRepeat(e.target.value)}
            aria-invalid={mismatch}
          />
        </Field>
      </div>
      {mismatch && <p className="mt-1.5 text-xs text-err">{t("settings.backup.export.mismatch")}</p>}

      <Hint tone="warn" icon={<ShieldAlert className="h-3.5 w-3.5" />} className="mt-3 max-w-xl">
        {t("settings.backup.export.warning")}
      </Hint>

      {exp.isError && <ErrorAlert className="mt-3">{backupErrorText(exp.error, t)}</ErrorAlert>}

      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Button variant="primary" disabled={!canExport} onClick={() => exp.mutate()}>
          <Download className="h-3.5 w-3.5" />
          {exp.isPending ? t("settings.backup.export.downloading") : t("settings.backup.export.download")}
        </Button>
        {saved && (
          <span className="text-sm text-ok">
            {t("settings.backup.export.done")} <span className="font-mono text-xs">{saved}</span>
          </span>
        )}
      </div>
    </Card>
  );
}
