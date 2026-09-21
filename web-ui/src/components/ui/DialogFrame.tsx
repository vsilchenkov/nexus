import { useEffect, useRef, type KeyboardEvent, type ReactNode, type RefObject } from "react";

import { cn } from "../../lib/cn";

type Props = {
  onClose: () => void;
  // labelledBy — id заголовка окна: по нему экранный диктор называет диалог.
  // Обязателен: безымянный диалог диктор объявит просто «диалог».
  labelledBy: string;
  describedBy?: string;
  // className — классы самого окна (рамка, фон, размеры); подложка общая.
  className?: string;
  children: ReactNode;
};

// DialogFrame — подложка и окно модального диалога с семантикой WAI-ARIA
// (§104.8): role="dialog", aria-modal, подпись по заголовку, фокус внутрь окна
// при открытии и обратно на источник при закрытии, Tab не уходит за окно.
// На нём стоят общий Modal и самописные окна Настроек — отдельные копии этих
// правил уже разъехались однажды: ни одно окно не было помечено диалогом.
//
// Закрытие — ТОЛЬКО явным действием (Esc / крестик / кнопки, §21): клик по
// подложке не закрывает, иначе случайный клик мимо окна терял данные формы.
export function DialogFrame({ onClose, labelledBy, describedBy, className, children }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  useEscapeClose(onClose);
  useDialogFocus(ref);

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4">
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        aria-describedby={describedBy}
        tabIndex={-1}
        onKeyDown={(e) => trapTab(e, ref.current)}
        className={cn("outline-none", className)}
      >
        {children}
      </div>
    </div>
  );
}

// useEscapeClose — Esc закрывает окно, если его не обработал вложенный слой.
//
// Выпадающие списки и подсказки внутри окна (Radix: Popover, Select, Tooltip)
// ловят Esc раньше — в capture-фазе на document — и, закрываясь, помечают
// событие preventDefault. Без этой проверки один Esc закрывал и список, и всё
// окно: в «Повторе за период» терялись выбранные период и фильтры (стенд §104.8).
// Граница: два наших окна друг над другом (Confirm поверх Modal) по-прежнему
// закрываются одним Esc оба — их обработчики равноправны на window.
function useEscapeClose(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}

// useDialogFocus — фокус в окно при открытии и назад при закрытии.
//
// Поле с autoFocus фокусируется при монтировании раньше этого эффекта — его не
// перебиваем. Назад фокус возвращается, только если после закрытия он никуда
// не ушёл (остался на body): закрытие переходом на другую страницу источник и
// так унесло, а чужой фокус отбирать нельзя.
function useDialogFocus(ref: RefObject<HTMLDivElement | null>) {
  useEffect(() => {
    const origin = document.activeElement;
    const el = ref.current;
    if (el && !el.contains(document.activeElement)) el.focus();
    return () => {
      const lost = document.activeElement === null || document.activeElement === document.body;
      if (lost && origin instanceof HTMLElement && origin.isConnected) origin.focus();
    };
  }, [ref]);
}

const TABBABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), ' +
  'select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

// trapTab — Tab и Shift+Tab ходят по кругу внутри окна.
//
// Вмешиваемся только на краях: у первого и последнего элемента окна (и у самого
// окна). Событие приходит и из порталов (выпадающие списки внутри окна живут в
// body, но React всплывает по своему дереву) — там фокус не на краю окна, и
// браузер ведёт его сам.
function trapTab(e: KeyboardEvent<HTMLDivElement>, dialog: HTMLDivElement | null) {
  if (e.key !== "Tab" || !dialog) return;
  const active = document.activeElement;
  if (active !== dialog && !dialog.contains(active)) return; // фокус в портале
  const items = Array.from(dialog.querySelectorAll<HTMLElement>(TABBABLE));
  const first = items[0];
  const last = items[items.length - 1];
  if (!first || !last) {
    e.preventDefault();
    dialog.focus();
  } else if (e.shiftKey && (active === first || active === dialog)) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && active === last) {
    e.preventDefault();
    first.focus();
  }
}
