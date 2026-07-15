// Глобальный setup vitest (§51.8): матчеры jest-dom (toBeInTheDocument,
// toHaveClass и т.п.) для RTL-тестов. Подключается через setupFiles в
// vitest.config.ts.
import "@testing-library/jest-dom/vitest";
