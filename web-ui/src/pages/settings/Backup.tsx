import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { api } from "../../api/client";
import { ErrorAlert } from "../../components/ui";
import { ExportCard } from "../../components/backup/ExportCard";
import { RestoreCard } from "../../components/backup/RestoreCard";
import type { BackupStatus } from "../../components/backup/types";

// BackupPanel — «Настройки → Резервная копия» (§110): выгрузка всей
// конфигурации в зашифрованный паролем файл и восстановление из него на
// чистом экземпляре. Только admin (вкладка и маршрут — в Settings.tsx).
export function BackupPanel() {
  const { t } = useTranslation();
  const status = useQuery({
    queryKey: ["backup-status"],
    queryFn: () => api.get<BackupStatus>("/api/backup/status"),
  });

  return (
    <div className="space-y-5">
      <header>
        <h2 className="text-lg font-semibold">{t("settings.backup.title")}</h2>
        <p className="mt-1 text-sm text-fg-muted">{t("settings.backup.subtitle")}</p>
      </header>
      {status.isLoading && <div className="text-fg-muted">{t("common.loading")}</div>}
      {status.isError && <ErrorAlert />}
      {status.data && (
        <>
          <ExportCard status={status.data} />
          <RestoreCard status={status.data} />
        </>
      )}
    </div>
  );
}
