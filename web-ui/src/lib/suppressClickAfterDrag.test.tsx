import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useSuppressClickAfterDrag } from "./suppressClickAfterDrag";

// §89.8: подавление click'а, который браузер шлёт по элементу после drop.
// Слушатель НАТИВНЫЙ и на document в фазе захвата — только так он отрабатывает
// раньше действия по умолчанию (переход по href) независимо от того, дойдёт ли
// событие до React после реконсиляции dnd-kit.

function clickOn(el: HTMLElement) {
  const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
  el.dispatchEvent(ev);
  return ev;
}

describe("useSuppressClickAfterDrag", () => {
  it("взведённое подавление гасит ровно один клик", () => {
    const el = document.createElement("a");
    document.body.append(el);
    const seen: string[] = [];
    el.addEventListener("click", () => seen.push("bubbled"));

    const { result } = renderHook(() => useSuppressClickAfterDrag());
    act(() => result.current.arm());

    const first = clickOn(el);
    expect(first.defaultPrevented).toBe(true);
    expect(seen).toEqual([]); // до обработчиков элемента событие не дошло

    // Одноразовость: следующий клик — обычный, иначе после драга интерфейс
    // «залипал» бы на один клик в непредсказуемом месте.
    const second = clickOn(el);
    expect(second.defaultPrevented).toBe(false);
    expect(seen).toEqual(["bubbled"]);

    el.remove();
  });

  it("disarm снимает взведение, если клика после драга не случилось", () => {
    const el = document.createElement("a");
    document.body.append(el);

    const { result } = renderHook(() => useSuppressClickAfterDrag());
    act(() => result.current.arm());
    act(() => result.current.disarm());

    expect(clickOn(el).defaultPrevented).toBe(false);
    el.remove();
  });

  it("повторный arm не плодит слушателей", () => {
    const el = document.createElement("a");
    document.body.append(el);

    const { result } = renderHook(() => useSuppressClickAfterDrag());
    act(() => {
      result.current.arm();
      result.current.arm();
    });

    expect(clickOn(el).defaultPrevented).toBe(true);
    // Если бы слушателей стало два, второй клик тоже был бы погашен.
    expect(clickOn(el).defaultPrevented).toBe(false);
    el.remove();
  });

  // Размонтирование во время драга иначе оставило бы слушатель на document
  // навсегда — он бы съедал по клику при каждом таком случае.
  it("размонтирование снимает слушатель", () => {
    const el = document.createElement("a");
    document.body.append(el);

    const { result, unmount } = renderHook(() => useSuppressClickAfterDrag());
    act(() => result.current.arm());
    unmount();

    expect(clickOn(el).defaultPrevented).toBe(false);
    el.remove();
  });
});
