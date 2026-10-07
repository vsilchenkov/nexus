import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { AlertTriangle, ChevronDown, Pause } from "lucide-react";

import type { KafkaOverview } from "../../api/client";
import { cn } from "../../lib/cn";
import { withNodeTab } from "../../lib/nodeTabUrl";

// PausedBacklog — вторая строка карточки «Текущий lag» (§113.7): очередь узлов
// на паузе в nexus.async.paused.
//
// Это не отставание обработки: сообщения ждут по замыслу и доставятся после
// возобновления узла. До §113 они входили в lag и зажигали баннер «Высокий
// consumer lag», хотя основной поток был пуст. Поэтому строка нейтральная и
// появляется только при непустой очереди.
//
// Настораживает одна ситуация: очередь есть, а узлов на паузе нет. Сразу после
// снятия паузы это нормально — очередь разбирается раундом прохода (ревизия
// §113: lag кешируется 30 с и убывает по мере доставки), а надолго — значит,
// разбирать некому (sweeper выключен или завис). Различить эти случаи по
// одному снимку нельзя, поэтому строка warn-тоном говорит, чего ждать и когда
// проверять Sender, а не утверждает «не разбирается»; алерт
// NexusPausedBacklogStuck ждёт те же 30 минут. Если список узлов не прочитан,
// вывода не делаем — показываем только число.
//
// §114.5: брокер не ответил — очередь паузы не прочитана, и 0 в paused_backlog
// не значит «пусто». Тогда строка есть всегда: приглушённое «отложено: —» с
// подсказкой, а не пустота, которую не отличить от пустой очереди.
export function PausedBacklog({ ov }: { ov: KafkaOverview }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  if (ov.paused_backlog_available === false) {
    return (
      <div className="mt-1.5 flex items-center gap-1 text-[11.5px] text-fg-subtle" title={t("kafka.paused.unavailable_hint")}>
        <Pause className="h-3.5 w-3.5 shrink-0" aria-hidden />
        <span>{t("kafka.paused.unavailable")}</span>
      </div>
    );
  }
  const backlog = ov.summary.paused_backlog ?? 0;
  if (backlog <= 0) return null;
  const n = backlog.toLocaleString();

  if (ov.paused_nodes_available && ov.paused_nodes_total === 0) {
    return (
      <div role="status" className="mt-1.5 flex items-start gap-1 text-[11.5px] text-warn">
        <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" aria-hidden />
        <span>{t("kafka.paused.stuck", { n })}</span>
      </div>
    );
  }

  return (
    <div className="mt-1.5 text-[11.5px] text-fg-muted">
      <div className="flex flex-wrap items-center gap-1" title={t("kafka.paused.hint")}>
        <Pause className="h-3.5 w-3.5 shrink-0" aria-hidden />
        <span>{t("kafka.paused.backlog", { n })}</span>
        {ov.paused_nodes_available && (
          <>
            <span aria-hidden>·</span>
            <button
              type="button"
              aria-expanded={open}
              onClick={() => setOpen((v) => !v)}
              className="inline-flex items-center gap-0.5 hover:text-fg"
            >
              {t("kafka.paused.nodes", { n: ov.paused_nodes_total })}
              <ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} aria-hidden />
            </button>
          </>
        )}
      </div>
      {open && <PausedNodeList ov={ov} />}
    </div>
  );
}

// PausedNodeList — узлы на паузе, каждый ведёт на свою вкладку «Очередь».
//
// Именно <Link> (урок §79): из мониторинга уходят смотреть очередь узла в
// новой вкладке — Ctrl+клик, средняя кнопка и контекстное меню должны работать.
// ?team= в адресе нет: команду переключает страница узла по самому узлу (§58).
function PausedNodeList({ ov }: { ov: KafkaOverview }) {
  const { t } = useTranslation();
  const rest = ov.paused_nodes_total - ov.paused_nodes.length;
  const search = `?${withNodeTab(new URLSearchParams(), "queue")}`;
  return (
    <ul className="mt-1 space-y-0.5 pl-[18px]">
      {ov.paused_nodes.map((node) => (
        <li key={node.id}>
          <Link
            to={{ pathname: `/nodes/${node.id}`, search }}
            className="font-mono text-[11px] hover:text-accent hover:underline"
          >
            {node.team} / {node.path}
          </Link>
        </li>
      ))}
      {rest > 0 && <li className="text-fg-subtle">{t("kafka.paused.more", { n: rest })}</li>}
    </ul>
  );
}
