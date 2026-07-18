import { useState } from "react";
import { Check, Share2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "../ui";
import { copyToClipboard } from "../../lib/clipboard";
import { nodePageUrl } from "../../lib/nodeShare";

// ShareNodeButton — кнопка «Поделиться» (§58, п.1): копирует ссылку на страницу
// узла в UI (/nodes/:id) в буфер обмена и на ~1.5с показывает подтверждение.
// Ссылка team-независима — открывший её коллега авто-переключится на команду
// узла (useEnsureNodeTeam, §58 п.2). Доступна ВСЕМ ролям: шаринг ссылки не
// мутирует данные, поэтому кнопка не гейтится по роли (в отличие от edit/copy).
export function ShareNodeButton({ nodeId, sm }: { nodeId: string; sm?: boolean }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  async function share() {
    if (await copyToClipboard(nodePageUrl(nodeId))) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    }
  }

  const iconCls = sm ? "h-3.5 w-3.5" : "h-4 w-4";
  return (
    <Button sm={sm} variant="ghost" onClick={share} title={t("node.actions.share")}>
      {copied ? (
        <Check className={`${iconCls} text-ok`} />
      ) : (
        <Share2 className={iconCls} />
      )}
      {copied ? t("common.copied") : t("node.actions.share")}
    </Button>
  );
}
