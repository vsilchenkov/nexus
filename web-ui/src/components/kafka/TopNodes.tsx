import { useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";
import { Link } from "react-router-dom";

import type { KafkaByNode } from "../../api/client";
import { Card, Chip, Pill } from "../ui";
import { fmtNum } from "../../lib/format";
import { withLogsWindow } from "../../lib/nodeTabUrl";

// NodeCell — путь узла: ссылка на журнал этого узла в ТОМ ЖЕ окне, что
// показывает экран Kafka (§98.2), либо просто текст.
//
// Текстом строка остаётся, когда бэкенд не сопоставил путь ровно одному узлу:
// узел удалён, путь занят несколькими командами, либо это метка-заглушка
// `<unresolved>` (§94.8). Ссылка «в никуда» хуже её отсутствия.
//
// Именно <Link>, а не onClick (урок §79): у ссылки работают Ctrl+клик, средняя
// кнопка мыши и контекстное меню «Открыть в новой вкладке» — а из мониторинга
// уходят смотреть журнал именно так, не теряя текущий экран.
function NodeCell({ path, nodeId, from, to }: NodeCellProps) {
  const { t } = useTranslation();
  if (!nodeId) {
    // §98.2-доп: молчаливое отсутствие ссылки читается как «ссылки не работают»
    // — так и было доложено со стенда. Причина же в данных: Prometheus хранит
    // метку `node` вечно, поэтому в топ попадают пути УДАЛЁННЫХ и
    // ПЕРЕИМЕНОВАННЫХ узлов (а ещё одноимённые пути двух команд и заглушка
    // §94.8). Ссылку из этого не собрать, но сказать, почему её нет, обязаны.
    return (
      <span className="font-mono text-xs text-fg-muted" title={t("kafka.node_unresolved")}>
        {path}
      </span>
    );
  }
  // onlyErrors=false и в блоке ошибок тоже: строка top-failures даёт число
  // ошибок за период, а в журнале смотрят их РЯДОМ с успешными доставками —
  // иначе не видно, всплеск это или узел вообще перестал отвечать.
  const search = withLogsWindow(new URLSearchParams(), { from, to, onlyErrors: false });
  return (
    <Link
      to={{ pathname: `/nodes/${nodeId}`, search: `?${search}` }}
      className="font-mono text-xs hover:text-accent hover:underline"
    >
      {path}
    </Link>
  );
}

type NodeCellProps = {
  path: string;
  nodeId?: string;
  // Границы периода экрана Kafka, мс. Журнал открывается на том же окне —
  // иначе после клика по строке «топ по ошибкам за час» окно пришлось бы
  // выставлять заново.
  from: number;
  to: number;
};

// TopNodes — два блока side-by-side (§5.7 spec): top producers и top failures.
export function TopNodes({ data, from, to }: { data: KafkaByNode; from: number; to: number }) {
  const { t } = useTranslation();
  return (
    <div className="grid grid-cols-1 gap-3.5 lg:grid-cols-2">
      <Card className="p-0">
        <div className="border-b border-line px-4 py-2.5 text-[13.5px] font-semibold">
          {t("kafka.top_producers.title")}
        </div>
        <table className="w-full text-[12.5px]">
          <thead>
            <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.node")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.messages")}</th>
              <th className="px-3 py-2 font-medium">{t("kafka.top_producers.share")}</th>
            </tr>
          </thead>
          <tbody>
            {data.top_producers.map((p) => (
              <tr key={p.node_path} className="border-b border-line last:border-0 hover:bg-bg-muted">
                <td className="px-3 py-2">
                  <NodeCell path={p.node_path} nodeId={p.node_id} from={from} to={to} />
                </td>
                <td className="px-3 py-2">{fmtNum(p.produced)}</td>
                <td className="px-3 py-2 text-fg-muted">{(p.share * 100).toFixed(1)}%</td>
              </tr>
            ))}
            {data.top_producers.length === 0 && (
              <tr>
                <td colSpan={3} className="px-3 py-6 text-center text-fg-muted">
                  {t("kafka.top_producers.empty")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Card>

      <Card className="p-0">
        <div className="border-b border-line px-4 py-2.5 text-[13.5px] font-semibold">
          {t("kafka.top_failures.title")}
        </div>
        {data.top_failures.length === 0 ? (
          <div className="flex flex-col items-center gap-2 px-4 py-8 text-center text-[12.5px] text-ok">
            <CheckCircle2 className="h-7 w-7" />
            {t("kafka.top_failures.empty")}
          </div>
        ) : (
          <table className="w-full text-[12.5px]">
            <thead>
              <tr className="border-b border-line text-left text-[11px] uppercase tracking-wide text-fg-muted">
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.node")}</th>
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.errors")}</th>
                <th className="px-3 py-2 font-medium">{t("kafka.top_failures.rate")}</th>
              </tr>
            </thead>
            <tbody>
              {data.top_failures.map((f) => {
                const pct = f.rate * 100;
                const row = pct > 5 ? "bg-err/10" : pct > 1 ? "bg-warn/10" : "";
                return (
                  <tr key={f.node_path} className={`border-b border-line last:border-0 ${row}`}>
                    <td className="px-3 py-2">
                      <NodeCell path={f.node_path} nodeId={f.node_id} from={from} to={to} />
                    </td>
                    <td className="px-3 py-2">{fmtNum(f.failed)}</td>
                    <td className="px-3 py-2">
                      {pct > 1 ? (
                        <Pill tone={pct > 5 ? "err" : "warn"}>{pct.toFixed(1)}%</Pill>
                      ) : (
                        <Chip>{pct < 0.001 ? "<0.001" : pct.toFixed(3)}%</Chip>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
