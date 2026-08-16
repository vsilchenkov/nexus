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
