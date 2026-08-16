import { useCallback, useEffect, useRef } from "react";

// §89.8: гашение клика, который браузер шлёт по элементу после drag-and-drop.
//
// Почему НЕ React-обработчик (onClick/onClickCapture на строке или на секции).
// Живой прогон на стенде показал: после drop наш React-обработчик не вызывается
// ВООБЩЕ, хотя нативный click до `document` доходит (проверено слушателем в фазе
// захвата). Причина — реконсиляция: dnd-kit переставляет узлы списка, и к
// моменту доставки события React уже не находит для цели путь по дереву фиберов,
// то есть синтетическое событие не диспатчится. Действие по умолчанию при этом
// никуда не девается: браузер уходит по href перетащенной ссылки, а зеркало §76
// применяет появившийся `?team=` и переключает команду сессии. Симптом выглядит
// как «перетащил избранное — оказался в другой команде».
//
// Нативный слушатель на document в фазе ЗАХВАТА срабатывает раньше любых
// обработчиков внутри дерева и не зависит от того, что React решит про фиберы.

// SuppressClickAfterDrag — управление одноразовым подавлением.
export type SuppressClickAfterDrag = {
  // arm — вооружить: следующий click будет погашен. Зовётся на старте драга.
  arm: () => void;
  // disarm — снять взведение. Зовётся на любом новом взаимодействии
  // пользователя: если после драга клика так и не случилось, подавление не
  // должно съесть чей-то следующий клик.
  disarm: () => void;
};

export function useSuppressClickAfterDrag(): SuppressClickAfterDrag {
  const handlerRef = useRef<((e: Event) => void) | null>(null);

  const disarm = useCallback(() => {
    if (!handlerRef.current) return;
    document.removeEventListener("click", handlerRef.current, true);
    handlerRef.current = null;
  }, []);

  const arm = useCallback(() => {
    if (handlerRef.current) return;
    const handler = (e: Event) => {
      e.preventDefault();
      e.stopPropagation();
      disarm(); // одноразовое: гасим ровно click, завершающий этот драг
    };
    handlerRef.current = handler;
    document.addEventListener("click", handler, true);
  }, [disarm]);

  // Размонтирование во время драга оставило бы слушатель на document навсегда —
  // он бы съедал по клику на каждый такой случай.
  useEffect(() => disarm, [disarm]);

  return { arm, disarm };
}
