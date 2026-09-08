import { expect } from "vitest";

import { ABOVE_STRETCH, STRETCH_HOST, STRETCHED_LINK } from "../lib/stretchedLink";

// Проверки контракта растянутой ссылки (lib/stretchedLink.ts).
//
// Чего эти проверки НЕ делают: они не доказывают, что кликабельна вся строка.
// В jsdom нет раскладки — getBoundingClientRect возвращает нули, CSS не
// применяется, псевдоэлементов в DOM не существует вовсе. Тест, который «кликает
// по краю строки», был бы зелёным по неверной причине.
//
// Что они делают: держат в DOM тот самый классовый и структурный контракт, из
// которого в браузере получается оверлей по всей строке. Этого достаточно, чтобы
// поймать реальные регрессии — «ссылку вернули к ширине надписи», «опору
// переставили на ячейку», «сняли подъём со спарклайна». Фактическую зону
// проверяет стендовый прогон через document.elementFromPoint — тем же способом,
// которым дефект и был найден.

// expectStretchedTo — ссылка растянута ровно по host.
export function expectStretchedTo(link: HTMLElement, host: HTMLElement): void {
  for (const c of STRETCHED_LINK.split(" ")) expect(link).toHaveClass(c);
  expect(host).toHaveClass(STRETCH_HOST);

  // Между ссылкой и опорой не должно быть другого позиционированного элемента:
  // псевдоэлемент лёг бы на него, и зона схлопнулась бы до ячейки — то есть
  // вернулся бы исходный дефект, а классы при этом остались бы на месте.
  for (let el = link.parentElement; el && el !== host; el = el.parentElement) {
    expect(el.className).not.toMatch(/(^|\s)(relative|absolute|fixed|sticky)(\s|$)/);
  }
}

// expectLiftedAbove — элемент поднят над растянутой ссылкой и не является её
// предком (подняли соседа, а не саму зону).
export function expectLiftedAbove(el: HTMLElement, link: HTMLElement): void {
  const lifted = el.closest(`.${ABOVE_STRETCH.split(" ").join(".")}`) as HTMLElement | null;
  expect(lifted).toBeTruthy();
  for (const c of ABOVE_STRETCH.split(" ")) expect(lifted!).toHaveClass(c);
  expect(lifted!.contains(link)).toBe(false);
}
