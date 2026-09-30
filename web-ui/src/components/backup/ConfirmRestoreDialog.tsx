import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Button, ErrorAlert, Input, Modal } from "../ui";
import type { ImportPreview } from "./types";

// ConfirmRestoreDialog — последнее подтверждение восстановления (§110.9):
// администратор вводит адрес этого сервера. Сервер сверяет его с Host запроса
// (имя без порта и регистра) — случайный клик на соседней вкладке с боевым
// Nexus восстановление не запустит.
export function ConfirmRestoreDialog({
  preview,
  host,
  pending,
  error,
  onConfirm,
  onClose,
}: {
  preview: ImportPreview;
  host: string;
  pending: boolean;
  error: string | null;
  onConfirm: (host: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [typed, setTyped] = useState("");
  const e = preview.entities;
  const matches = typed.trim().toLowerCase() === host.toLowerCase();

  return (
    <Modal
      title={t("settings.backup.confirm.title")}
      onClose={pending ? () => {} : onClose}
      className="max-w-md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" disabled={!matches || pending} onClick={() => onConfirm(typed.trim())}>
            {pending ? t("settings.backup.confirm.running") : t("settings.backup.confirm.submit")}
          </Button>
        </>
      }
    >
      {error && <ErrorAlert className="mb-3">{error}</ErrorAlert>}
      <p className="text-[13px]">
        {t("settings.backup.confirm.body", {
          host,
          teams: (e.teams?.create ?? 0) + (e.teams?.existing ?? 0),
          nodes: e.nodes?.create ?? 0,
        })}
      </p>
      <label htmlFor="backup-confirm-host" className="mb-1.5 mt-4 block text-xs text-fg-muted">
        {t("settings.backup.confirm.type_host")} <span className="font-mono">{host}</span>
      </label>
      <Input
        id="backup-confirm-host"
        mono
        autoComplete="off"
        value={typed}
        onChange={(ev) => setTyped(ev.target.value)}
        placeholder={host}
        disabled={pending}
      />
      {pending && <p className="mt-3 text-xs text-fg-muted">{t("settings.backup.confirm.running_hint")}</p>}
    </Modal>
  );
}
