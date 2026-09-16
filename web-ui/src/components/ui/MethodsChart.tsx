import { useState } from "react";
import { useTranslation } from "react-i18next";
import { ExternalLink } from "lucide-react";
import { Link } from "react-router-dom";

import { cn } from "../../lib/cn";
import { fmtNum, middleEllipsis } from "../../lib/format";
import { ChartTooltip, type TooltipSeries } from "./ChartTooltip";
import { Tooltip } from "./Tooltip";

export type MethodStat = { method: string; total: number; delivered: number };

// Цвета серий — те же, что у графика трафика: рейтинг читается рядом с ним и
// обязан кодировать «доставлено/ошибки» одинаково.
const C_OK = "#5b9bf0";
const C_ERR = "#e85d5c";

// METHOD_ROWS_DEFAULT / METHOD_ROWS_MAX — §102.5: восемь строк помещаются в карточку без
// прокрутки и покрывают типичный passthrough-узел; «Показать ещё» раскрывает до
// двадцати — столько отдаёт сервер (MethodsLimit).
export const METHOD_ROWS_DEFAULT = 8;
export const METHOD_ROWS_MAX = 20;

// LABEL_CHARS — бюджет символов на имя метода в строке. Считается посимвольно,
// а не CSS-обрезкой: у символьного правила есть тест, у раскладки в jsdom его
// быть не может (урок §100).
const LABEL_CHARS = 46;

// Row — то, что реально рисуется: строка метода либо свёртка «Остальные».
// «Остальные» не приходит с сервера — она ВЫЧИТАЕТСЯ из kpi.total, см. §102.3.
type Row = {
  key: string;
  method: string; // "" для строки «(без подпути)» и для свёртки
  label: string;
  total: number;
  delivered: number;
  rest?: { count: number; more: boolean };
};

/**
 * MethodsChart — рейтинг «Запросы по методам» (§102).
 *
 * Презентационный: данные, знаменатель доли и построитель адресов приходят
 * пропами. Единица счёта — ЗАПИСЬ, та же, что у KPI, поэтому Σ(data) сходится с
 * `total` и остаток можно вычесть, а не запрашивать отдельно.
 *
 * Строка — НАСТОЯЩАЯ ссылка (`<a href>`), а не кнопка с обработчиком: у неё есть
 * адрес, значит работают Ctrl+клик, средняя кнопка и «Открыть в новой вкладке»
 * (§100.2). Исключения — строка с пустым методом и свёртка «Остальные»: фильтра
 * по ним в журнале нет, и ссылка вела бы в журнал БЕЗ фильтра, то есть показала
 * бы все записи под видом отобранных.
 */
export function MethodsChart({
  data,
  total,
  delivered,
  truncated = false,
  hrefFor,
  className,
}: {
  data: MethodStat[];
  /** kpi.total за то же окно и под теми же фильтрами — база доли и «Остальных». */
  total: number;
  /** kpi.delivered — из него вычитается доставленное показанных строк. */
  delivered: number;
  /** methods_truncated: сервер отдал не все методы (их больше лимита). */
  truncated?: boolean;
  /** Адрес журнала по методу. undefined → строка не ссылка. */
  hrefFor?: (method: string) => string | undefined;
  className?: string;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);

  // §102.2: если свернуть осталось РОВНО один метод, он показывается сам, а не
  // строкой «Остальные · 1 метод»: свёртка одного элемента прячет имя и ничего
  // не экономит. При усечении на сервере это правило не действует — за лишней
  // строкой там стоит неизвестный хвост, а не один метод.
  const rowsLimit = expanded ? METHOD_ROWS_MAX : METHOD_ROWS_DEFAULT;
  const visibleFor = (limit: number) =>
    data.slice(0, data.length === limit + 1 && !truncated ? data.length : limit);
  const visible = visibleFor(rowsLimit);
  const shownSum = visible.reduce((s, m) => s + m.total, 0);
  const shownDelivered = visible.reduce((s, m) => s + m.delivered, 0);

  const rows: Row[] = visible.map((m, i) => ({
    key: `${i}:${m.method}`,
    method: m.method,
    label: m.method ? middleEllipsis(m.method, LABEL_CHARS) : t("metrics.methods.empty_method"),
    total: m.total,
    // Кламп на случай рассинхрона источника: красный сегмент отрицательной
    // ширины растянул бы полосу на всю карточку.
    delivered: Math.min(m.delivered, m.total),
  }));

  // §102.3: остаток = «Всего» − Σ(показанных). Ноль или минус означает, что
  // разбивка и KPI разъехались (внешняя таблица §64, приблизительный счёт,
  // долив писателя между запросами) — тогда строки нет вовсе: нулевая строка
  // рядом с пометкой «есть ещё методы» была бы прямой ложью на экране.
  const restTotal = total - shownSum;
  const restCount = data.length - visible.length;
  if (restTotal > 0 && (restCount > 0 || truncated)) {
    rows.push({
      key: "rest",
      method: "",
      label: t("metrics.methods.others"),
      total: restTotal,
      delivered: Math.max(0, Math.min(delivered - shownDelivered, restTotal)),
      rest: { count: restCount, more: truncated },
    });
  }

  const max = Math.max(1, ...rows.map((r) => r.total));
  const canExpand = !expanded && data.length > visible.length;
  // Свернуть предлагается только если это что-то изменит: при девяти методах
  // свёрнутый вид показывает все девять (правило «последний метод сам за себя»),
  // и кнопка «Свернуть» была бы обманом — она ничего бы не убрала.
  const canCollapse = expanded && visible.length > visibleFor(METHOD_ROWS_DEFAULT).length;

  return (
    <div className={cn("w-full", className)}>
      <div className="space-y-2">
        {rows.map((row) => (
          <MethodRow
            key={row.key}
            row={row}
            max={max}
            total={total}
            href={row.rest || !row.method ? undefined : hrefFor?.(row.method)}
          />
        ))}
      </div>

      {/* Кнопка стоит ВНЕ строк-ссылок: интерактив не вкладывается в интерактив,
          и адреса у «Показать ещё» нет — это состояние вида, а не переход.
          Раскрытие обратимо: двадцать строк вытесняют латентность и ёмкость за
          край экрана, и вернуться к восьми должно быть чем, а не перезагрузкой. */}
      {(canExpand || canCollapse) && (
        <div className="mt-3 flex justify-end">
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="rounded-sm border border-line px-2 py-1 text-xs text-fg-muted hover:border-line-strong hover:text-fg"
          >
            {t(canExpand ? "metrics.methods.show_more" : "metrics.methods.show_less")}
          </button>
        </div>
      )}
    </div>
  );
}

function MethodRow({
  row,
  max,
  total,
  href,
}: {
  row: Row;
  max: number;
  total: number;
  href?: string;
}) {
  const { t } = useTranslation();
  const errors = Math.max(0, row.total - row.delivered);
  const okW = (row.delivered / max) * 100;
  const errW = (errors / max) * 100;
  const pct = total > 0 ? Math.round((row.total / total) * 1000) / 10 : 0;

  const series: TooltipSeries[] = [
    { marker: "bar", color: C_OK, name: t("metrics.tooltip.delivered"), value: fmtNum(row.delivered) },
  ];
  if (errors > 0) {
    series.push({ marker: "bar", color: C_ERR, name: t("metrics.tooltip.errors"), value: fmtNum(errors) });
  }

  // Заголовок тултипа — ПОЛНОЕ имя (в строке оно сокращено), а для служебных
  // строк — объяснение, почему они никуда не ведут.
  // Число свёрнутых методов известно не всегда: при methods_truncated и полном
  // раскрытии сервер не сказал, сколько их ещё. «более 0 методов» было бы
  // бессмыслицей, поэтому счётчик тогда просто не подписывается.
  const restCountLabel =
    row.rest && row.rest.count > 0
      ? row.rest.more
        ? t("metrics.methods.others_n_more", { n: row.rest.count })
        : t("metrics.methods.others_n", { n: row.rest.count })
      : "";
  const title = row.rest
    ? restCountLabel
      ? `${t("metrics.methods.others")} · ${restCountLabel}`
      : t("metrics.methods.others")
    : row.method || t("metrics.methods.empty_method");

  const body = (
    <>
      <div className="flex items-baseline justify-between gap-3">
        <span
          className={cn(
            "truncate font-mono text-[11.5px]",
            row.method || row.rest ? "text-fg-muted" : "text-fg-subtle",
          )}
        >
          {row.label}
          {restCountLabel && <span className="ml-1.5 text-fg-subtle">· {restCountLabel}</span>}
        </span>
        <span className="shrink-0 font-mono text-xs text-fg">{fmtNum(row.total)}</span>
      </div>
      <div className="mt-1 flex h-2 w-full overflow-hidden rounded-sm bg-bg-muted">
        <div
          className={cn("h-full", row.rest ? "bg-accent/30" : "bg-accent/60")}
          style={{ width: `${okW}%` }}
        />
        {/* Ошибки при малой доле схлопнулись бы в ноль пикселей — минимум 2px,
            иначе «красное есть в числах, но не видно в полосе». */}
        {errors > 0 && (
          <div className="h-full min-w-[2px] bg-err/70" style={{ width: `${errW}%` }} />
        )}
      </div>
    </>
  );

  const content = (
    <ChartTooltip
      title={title}
      primary={{ value: fmtNum(row.total), unit: t("metrics.tooltip.requests") }}
      series={series}
      footer={{ tone: "muted", text: t("metrics.methods.tooltip.share", { pct }) }}
      action={
        href
          ? { icon: <ExternalLink className="h-3 w-3" />, label: t("metrics.methods.open_logs") }
          : !row.rest && !row.method
            ? { label: t("metrics.methods.empty_method_hint") }
            : undefined
      }
    />
  );

  return (
    <Tooltip side="top" delayDuration={150} content={content}>
      {href ? (
        <Link to={href} className="block rounded-sm px-1 py-0.5 hover:bg-bg-muted/50">
          {body}
        </Link>
      ) : (
        <div className="block px-1 py-0.5">{body}</div>
      )}
    </Tooltip>
  );
}
