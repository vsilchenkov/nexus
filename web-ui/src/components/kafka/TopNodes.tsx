import { useTranslation } from "react-i18next";
import { CheckCircle2 } from "lucide-react";
import { Link } from "react-router-dom";

import { cn } from "../../lib/cn";
import { STRETCH_HOST, STRETCHED_LINK } from "../../lib/stretchedLink";

import type { KafkaByNode } from "../../api/client";
import { Card, Chip, Pill } from "../ui";
import { fmtNum } from "../../lib/format";
import { withLogsWindow } from "../../lib/nodeTabUrl";

// UNRESOLVED_NODE — метка-заглушка Prometheus для запросов, узел которых не
// найден (§94.8, metrics.NodeUnresolved на сервере). Команды у неё нет никогда.
const UNRESOLVED_NODE = "<unresolved>";

// rowKey — React-ключ строки топа (§101.10). Строка — пара (команда, путь):
// одноимённые узлы разных команд дают две строки, и ключ по одному пути у них
// совпал бы.
function rowKey(r: { team?: string; node_path: string }): string {
  return `${r.team ?? ""}/${r.node_path}`;
}

// unlinkedReason — ключ подсказки строки без ссылки. Причины разные, и
// подсказка обязана назвать настоящую (§98.2, §101.10): «узел переименован»
// у заглушки <unresolved> или у рядов до разделения по командам была бы
// неправдой.
function unlinkedReason(path: string, team?: string): string {
  if (path === UNRESOLVED_NODE) return "kafka.node_placeholder";
  if (!team) return "kafka.node_legacy";
  return "kafka.node_unresolved";
}

// TeamChip — приглушённый слог команды справа от пути (§101.10).
//
// Слог приходит с сервера: экран админский, а админ может не состоять в команде
// узла, так что клиентский резолв по членствам дал бы пустоту. Строка рядов до
// разделения по командам получает «без команды»; у заглушки <unresolved> чипа
// нет — команды у неё не бывает по определению.
function TeamChip({ path, team }: { path: string; team?: string }) {
  const { t } = useTranslation();
  if (path === UNRESOLVED_NODE) return null;
  // Пробел — часть доступного имени ссылки: без него скринридер прочтёт
  // «telegrammessengers», и две строки одного пути звучали бы одинаково.
  return (
    <>
      {" "}
      <Chip className="px-2 py-0 font-sans">{team || t("kafka.no_team")}</Chip>
    </>
  );
}

// NodeCell — узел строки: путь и команда (§101.10), ссылка на журнал этого узла
// в ТОМ ЖЕ окне, что показывает экран Kafka (§98.2), либо просто текст.
//
// Текстом строка остаётся, когда бэкенд не сопоставил пару (команда, путь)
// узлу: узел переименован, удалён или перенесён, у строки нет команды (ряды до
// §101), либо это метка-заглушка `<unresolved>` (§94.8). Ссылка «в никуда» хуже
// её отсутствия.
//
// Именно <Link>, а не onClick (урок §79): у ссылки работают Ctrl+клик, средняя
// кнопка мыши и контекстное меню «Открыть в новой вкладке» — а из мониторинга
// уходят смотреть журнал именно так, не теряя текущий экран.
function NodeCell({ path, team, nodeId, from, to }: NodeCellProps) {
  const { t } = useTranslation();
  if (!nodeId) {
    // §98.2-доп: молчаливое отсутствие ссылки читается как «ссылки не работают»
    // — так и было доложено со стенда. Причина же в данных: Prometheus хранит
    // метки вечно, поэтому в топ попадают пути УДАЛЁННЫХ и ПЕРЕИМЕНОВАННЫХ
    // узлов, ряды до разделения по командам и заглушка §94.8. Ссылку из этого не
    // собрать, но сказать, почему её нет, обязаны.
    return (
      <span className="font-mono text-xs text-fg-muted" title={t(unlinkedReason(path, team))}>
        {path}
        <TeamChip path={path} team={team} />
      </span>
    );
  }
  // onlyErrors=false и в блоке ошибок тоже: строка top-failures даёт число
  // ошибок за период, а в журнале смотрят их РЯДОМ с успешными доставками —
  // иначе не видно, всплеск это или узел вообще перестал отвечать.
  //
  // ?team= в адресе нет (§101.10): команду узла переключает useEnsureNodeTeam
  // (§58) по самому узлу.
  const search = withLogsWindow(new URLSearchParams(), { from, to, onlyErrors: false });
  // Ссылка растянута по всей строке (§7.3, тот же приём, что на «Узлах»): до
  // этого строка подсвечивалась целиком, а кликалась одна колонка из трёх —
  // клик мимо надписи не делал ничего.
  return (
    <Link
      to={{ pathname: `/nodes/${nodeId}`, search: `?${search}` }}
      className={cn("font-mono text-xs hover:text-accent hover:underline", STRETCHED_LINK)}
    >
      {/* Путь поднят над псевдоэлементом, чтобы его можно было выделить мышью. */}
      <span className="relative z-[1]">{path}</span>
      <TeamChip path={path} team={team} />
    </Link>
  );
}

type NodeCellProps = {
  path: string;
  // Слог команды из метки ряда (§101.10); пуст у рядов до §101 и у заглушки.
  team?: string;
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
      {/* relative — страховка на случай, если браузер проигнорирует position на
          <tr>: без неё псевдоэлементы строк уехали бы к корню страницы (та же
          защита, что у таблицы «Узлов»). */}
      <Card className={cn("p-0", STRETCH_HOST)}>
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
              // Подсветка только у строк со ссылкой: у неразрешённого пути
              // (§98.2) клика нет, и подсвечивать его как кликабельный —
              // ровно то враньё, которое §98.2 и просил убрать.
              <tr
                key={rowKey(p)}
                className={cn(
                  "border-b border-line last:border-0",
                  p.node_id && cn(STRETCH_HOST, "hover:bg-bg-muted"),
                )}
              >
                <td className="px-3 py-2">
                  <NodeCell path={p.node_path} team={p.team} nodeId={p.node_id} from={from} to={to} />
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

      <Card className={cn("p-0", STRETCH_HOST)}>
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
                  <tr
                    key={rowKey(f)}
                    className={cn(
                      "border-b border-line last:border-0",
                      row,
                      f.node_id && cn(STRETCH_HOST, "hover:bg-bg-muted"),
                    )}
                  >
                    <td className="px-3 py-2">
                      <NodeCell path={f.node_path} team={f.team} nodeId={f.node_id} from={from} to={to} />
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
