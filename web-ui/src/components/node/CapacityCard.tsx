import { useTranslation } from "react-i18next";

import { type Node } from "../../api/client";
import { Card, LabelHint } from "../ui";
import { cn } from "../../lib/cn";
import { fmtNum } from "../../lib/format";
import { capacityShare, capacityTone, fmtCapacityDuration, fmtShare } from "../../lib/queueCapacity";
import { Link } from "react-router-dom";
import { useNodeTabTo } from "../../lib/nodeTabLink";

/**
 * CapacityCard — помещается ли узел в одну партицию Kafka (§84.8).
 *
 * Ни одного нового запроса: и число запросов, и p95, и длительность окна уже
 * пришли в ответе метрик. Это узловой срез диагностики §80.2 — общий экран
 * «кто держит очередь» остаётся за §80.
 *
 * Только у async-узлов: у sync очереди нет вовсе, и число было бы бессмысленным.
 */
export function CapacityCard({
  node,
  total,
  p95Ms,
  rangeMs,
}: {
  node: Node;
  total: number;
  p95Ms: number;
  rangeMs: number;
}) {
  const to = useNodeTabTo();
  const { t } = useTranslation();
  const units = t("metrics.capacity.units").split("|");

  if (node.root_method !== "requestAsync") return null;
  const share = capacityShare({ total, p95Ms, rangeMs });
  if (share === null) return null;

  const tone = capacityTone(share);
  const over = tone === "err";

  return (
    <Card>
      <div className="mb-2 flex items-center gap-1.5 text-sm font-semibold">
        {t("metrics.capacity.title")}
        <LabelHint
          content={
            <div className="max-w-xs space-y-1 text-left">
              <div>{t("metrics.capacity.hint")}</div>
              <div>{t("metrics.capacity.hint_formula")}</div>
            </div>
          }
        />
      </div>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span
          className={cn(
            "text-2xl font-semibold",
            tone === "err" ? "text-err" : tone === "warn" ? "text-warn" : "text-fg",
          )}
        >
          {over && "⛔ "}
          {fmtShare(share)}
        </span>
        <span className="text-xs text-fg-muted">
          {/* Единицы приходят готовыми внутри значений: у быстрого узла p95
              измеряется миллисекундами, у короткого окна период — минутами. */}
          {t("metrics.capacity.formula", {
            total: fmtNum(total),
            p95: fmtCapacityDuration(p95Ms, units),
            range: fmtCapacityDuration(rangeMs, units),
          })}
        </span>
      </div>
      {over && <p className="mt-2 text-xs text-err">{t("metrics.capacity.over")}</p>}
      {/* Ёмкость отвечает на вопрос «помещается ли узел», а не «сколько ждёт
          прямо сейчас» — на второй отвечает вкладка «Очередь», и ссылка на неё
          обязательна, иначе разбор упирается в тупик. */}
      <p className="mt-1 text-xs text-fg-muted">
        {t("metrics.capacity.see_queue")}{" "}
        {/* Выглядела ссылкой (text-accent + underline) и вела на ?tab=queue, но
            была кнопкой: Ctrl+клик открывал пустоту. */}
        <Link to={to.tab("queue")} className="text-accent underline-offset-2 hover:underline">
          {t("metrics.capacity.queue_link")}
        </Link>
      </p>
    </Card>
  );
}
