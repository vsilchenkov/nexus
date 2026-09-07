import { useState } from "react";
import { Check, Share2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { copyToClipboard } from "../../lib/clipboard";
import { Button } from "./Button";

// ShareLinkButton — кнопка «Поделиться»: кладёт готовую ссылку в буфер обмена и
// на ~1.5 с показывает подтверждение.
//
// Общая часть двух кнопок (§58 — страница узла, §98.3 — карточка отказа).
// Дублировать её нельзя: разъедется не поведение, а мелочи вроде длительности
// подтверждения и иконки, и две кнопки «Поделиться» в одном приложении начнут
// вести себя по-разному.
//
// Что именно шарится — решает вызывающая сторона: сборка адреса живёт рядом с
// экраном (nodeShare/rejectedShare), здесь только копирование и обратная связь.
//
// Доступна ВСЕМ ролям: копирование ссылки ничего не мутирует.
export function ShareLinkButton({
  url,
  sm,
  title,
}: {
  url: string;
  sm?: boolean;
  // title — подпись и tooltip; по умолчанию общий «Поделиться».
  title?: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const label = title ?? t("node.actions.share");

  async function share() {
    if (await copyToClipboard(url)) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    }
  }

  const iconCls = sm ? "h-3.5 w-3.5" : "h-4 w-4";
  return (
    <Button sm={sm} variant="ghost" onClick={share} title={label}>
      {copied ? <Check className={`${iconCls} text-ok`} /> : <Share2 className={iconCls} />}
      {copied ? t("common.copied") : label}
    </Button>
  );
}
