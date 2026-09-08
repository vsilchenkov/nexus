// Глобальный setup vitest (§51.8): матчеры jest-dom (toBeInTheDocument,
// toHaveClass и т.п.) для RTL-тестов. Подключается через setupFiles в
// vitest.config.ts.
import "@testing-library/jest-dom/vitest";

// ResizeObserver в jsdom отсутствует, а Radix позиционирует поповеры и тултипы
// через floating-ui, который его требует, — без заглушки любой тест, где
// открывается Popover, падает «ReferenceError: ResizeObserver is not defined»
// ещё до первого assert'а. Заглушка, а не полифилл: измерений в jsdom всё равно
// нет (все размеры нулевые), тестам нужен только факт наличия API.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

// scrollIntoView в jsdom не реализован, а cmdk (Command/CommandItem) зовёт его
// при подсветке активного пункта — без заглушки любой тест, открывающий
// комбобокс справочника (§24 HeadersField, §99 GroupField), падает
// «TypeError: e.scrollIntoView is not a function» уже после рендера списка.
// Заглушка, а не полифилл: прокрутки в jsdom нет, тестам нужен только факт
// наличия метода.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = function scrollIntoView() {};
}
