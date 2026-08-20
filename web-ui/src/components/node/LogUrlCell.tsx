import { useEffect, useRef, useState } from "react";

import { CopyButton, Popover, PopoverAnchor, PopoverContent } from "../ui";

// Задержка закрытия подсказки URL: столько миллисекунд курсор может находиться
// в зазоре между ячейкой и всплывающим блоком, не закрывая его.
const URL_POPOVER_CLOSE_DELAY_MS = 250;

/**
 * LogUrlCell — ячейка колонки «URL» в таблицах логов. Значение обрезается по
 * ширине колонки, а полный адрес показывается во всплывающем блоке с кнопкой
 * «Скопировать» (нативный title копировать не давал).
 *
 * Общий компонент для журнала логов узла (LogsTab) и карточки «Последние
 * запросы» на вкладке «Обзор» (OverviewTab): подсказка обязана вести себя
 * одинаково в обоих местах, а копия разъехалась бы с оригиналом.
 *
 * Почему Popover, а не Tooltip: строки таблицы плотные, и у КАЖДОЙ своя ячейка
 * URL. Radix Tooltip закрывается, едва курсор покидает триггер, а по дороге к
 * кнопке он проходит над соседней строкой — та перехватывает hover, подсказка
 * закрывается, и клик приходится уже по строке (проверено на стенде: запись
 * раскрывалась, буфер оставался пустым). Popover живёт своей жизнью: соседи его
 * не закрывают, а уход курсора гасит его с задержкой — успеть перевести мышь.
 *
 * ВАЖНО про раскладку: сам по себе `truncate` не обрежет ничего, если у таблицы
 * авто-раскладка — браузер просто расширит колонку под содержимое. Таблица,
 * которая использует эту ячейку, обязана быть `table-fixed` с заданными
 * ширинами колонок (см. LogsTab и OverviewTab).
 */
export function LogUrlCell({ url }: { url: string }) {
  const [open, setOpen] = useState(false);
  const closeTimer = useRef<number | null>(null);

  const cancelClose = () => {
    if (closeTimer.current !== null) {
      window.clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  };
  const scheduleClose = () => {
    cancelClose();
    closeTimer.current = window.setTimeout(() => setOpen(false), URL_POPOVER_CLOSE_DELAY_MS);
  };
  // Размонтирование по ходу подгрузки/фильтрации не должно оставлять таймер.
  useEffect(() => cancelClose, []);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      {/* Anchor, а не Trigger: у Trigger свой onClick, который перехватил бы
          клик по строке (раскрытие записи). Открываем только по наведению. */}
      <PopoverAnchor asChild>
        <span
          className="block truncate"
          onMouseEnter={() => {
            cancelClose();
            setOpen(true);
          }}
          onMouseLeave={scheduleClose}
        >
          {url}
        </span>
      </PopoverAnchor>
      <PopoverContent
        side="top"
        align="start"
        sideOffset={4}
        // Открытие по hover не должно уводить фокус с таблицы, а закрытие —
        // возвращать его на ячейку (иначе прыгает скролл длинного списка).
        onOpenAutoFocus={(e) => e.preventDefault()}
        onCloseAutoFocus={(e) => e.preventDefault()}
        onMouseEnter={cancelClose}
        onMouseLeave={scheduleClose}
        // Radix рендерит контент в портал, но React-события всплывают по
        // React-дереву — без этого клик по кнопке дошёл бы до onClick строки.
        onClick={(e) => e.stopPropagation()}
        className="flex max-w-[420px] items-start gap-2 px-2.5 py-2"
      >
        <span className="min-w-0 break-all font-mono text-[11px]">{url}</span>
        <CopyButton value={url} />
      </PopoverContent>
    </Popover>
  );
}
