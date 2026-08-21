import { Children, type ReactNode } from "react";

import { cn } from "../../lib/cn";
import { LabelHint } from "./LabelHint";

type Tone = "default" | "info" | "success" | "danger" | "warning";

const chipTones: Record<Tone, string> = {
  default: "bg-bg-muted text-fg-muted",
  info: "bg-accent/10 text-accent",
  success: "bg-ok/10 text-ok",
  danger: "bg-err/10 text-err",
  warning: "bg-warn/10 text-warn",
};

// Chip — компактный тег эталона (.chip).
export function Chip({
  tone = "default",
  children,
  className,
}: {
  tone?: Tone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[11px]",
        chipTones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

type PillTone = "ok" | "err" | "warn" | "muted";

const pillTones: Record<PillTone, string> = {
  ok: "bg-ok/10 text-ok",
  err: "bg-err/10 text-err",
  warn: "bg-warn/10 text-warn",
  muted: "bg-fg-subtle/10 text-fg-muted",
};

// Pill — статус-бейдж эталона (.pill).
export function Pill({
  tone,
  children,
  className,
}: {
  tone: PillTone;
  children: ReactNode;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2.5 py-0.5 text-[11px] font-medium",
        pillTones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

// Kpi — карточка показателя (.kpi). delta — подпись под значением.
// hint — необязательная расшифровка метрики (info-иконка у метки).
export function Kpi({
  label,
  value,
  delta,
  deltaTone,
  hint,
  className,
}: {
  label: ReactNode;
  value: ReactNode;
  delta?: ReactNode;
  deltaTone?: "up" | "down" | "muted";
  hint?: string;
  className?: string;
}) {
  const deltaCls =
    deltaTone === "up" ? "text-ok" : deltaTone === "down" ? "text-err" : "text-fg-muted";
  return (
    <div className={cn("rounded-md border border-line bg-bg px-4 py-3", className)}>
      <div className="mb-1.5 flex items-center gap-1 text-[11px] text-fg-muted">
        <span>{label}</span>
        {hint && <LabelHint content={hint} />}
      </div>
      <div className="text-[23px] font-semibold leading-none tracking-tight">{value}</div>
      {/* §44: строка delta резервируется всегда (nbsp-плейсхолдер) — высота
          карточки постоянна, интерфейс не прыгает при дозагрузке цифр. */}
      <div className={cn("mt-1 text-[11px]", deltaCls)}>{delta != null ? delta : " "}</div>
    </div>
  );
}

// KpiRow — сетка из KPI-карточек (по умолчанию 4 колонки).
//
// Одинокая плитка НЕ выкладывается сеткой. Часть плиток условна (у sync-узла
// нет «Ожидают отправки» — очереди не существует, §69.1), и когда остаётся
// одна, двухколоночная сетка отдаёт ей ровно половину ширины, а вторую половину
// оставляет пустой. Читается это как поломка вёрстки, а не как замысел.
// Поэтому единственная плитка получает собственную умеренную ширину: и на всю
// строку не растягивается (прежнее требование §69.1), и дыры рядом не делает.
export function KpiRow({
  children,
  cols = 4,
  single = "compact",
  className,
}: {
  children: ReactNode;
  cols?: 2 | 3 | 4;
  // single — что делать с ЕДИНСТВЕННОЙ отрисованной плиткой:
  //   "compact" (по умолчанию) — узкая карточка (max-w-xs). Подходит там, где
  //     соседние плитки просто не пришли и широкая карточка читалась бы как
  //     растянутый по ошибке блок;
  //   "full" — плитка занимает всю ширину строки. Для случаев, когда соседа не
  //     бывает В ПРИНЦИПЕ (у sync-узла нет очереди, а значит и плитки «Ожидают
  //     отправки»), и пустая половина строки выглядит как недогруженный экран.
  single?: "compact" | "full";
  className?: string;
}) {
  // Считаем ТОЛЬКО отрисованные плитки: условная `{flag && <Kpi/>}` даёт в
  // children значение false, и Children.count посчитал бы его тоже.
  const shown = Children.toArray(children).filter(Boolean).length;
  if (shown === 1) {
    return (
      <div className={cn(single === "full" ? "w-full" : "max-w-xs", className)}>{children}</div>
    );
  }
  const colsCls = cols === 2 ? "sm:grid-cols-2" : cols === 3 ? "sm:grid-cols-3" : "sm:grid-cols-4";
  return (
    <div className={cn("grid grid-cols-1 gap-3", colsCls, className)}>{children}</div>
  );
}

// SegOption — вариант переключателя. disabled НЕ прячет вариант, а гасит его:
// набор кнопок остаётся постоянным, поэтому ширина строки не скачет при смене
// соседнего контрола (§84: шаги, недоступные при коротком периоде).
export type SegOption<T extends string> = {
  value: T;
  label: ReactNode;
  disabled?: boolean;
  title?: string;
};

// Seg — сегментный переключатель эталона (.seg).
export function Seg<T extends string>({
  value,
  options,
  onChange,
  className,
}: {
  value: T;
  options: SegOption<T>[];
  onChange: (v: T) => void;
  className?: string;
}) {
  return (
    <div className={cn("inline-flex rounded-md bg-bg-muted p-0.5", className)}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          disabled={o.disabled}
          title={o.title}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded px-3 py-1 text-xs transition-colors",
            o.disabled
              ? "cursor-not-allowed text-fg-subtle opacity-40"
              : o.value === value
                ? "bg-bg-3 font-medium text-fg"
                : "text-fg-muted hover:text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

type HintTone = "info" | "warn" | "danger" | "muted";

const hintTones: Record<HintTone, string> = {
  info: "bg-accent/10 text-accent",
  warn: "bg-warn/10 text-warn",
  danger: "bg-err/10 text-err",
  muted: "bg-bg-muted text-fg-muted",
};

// Hint — поясняющая плашка эталона (.hint).
export function Hint({
  tone = "muted",
  icon,
  children,
  className,
}: {
  tone?: HintTone;
  icon?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex gap-2 rounded-md px-3 py-2.5 text-[11.5px] leading-relaxed",
        hintTones[tone],
        className,
      )}
    >
      {icon && <span className="mt-px shrink-0">{icon}</span>}
      <div>{children}</div>
    </div>
  );
}
