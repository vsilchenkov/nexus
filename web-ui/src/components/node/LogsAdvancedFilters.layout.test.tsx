import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { describe, expect, it, vi } from "vitest";

// Регресс: с перевода поля «Поиск» на SearchInput (§90.3) атом Input добавлял
// полю свои h-9 и рамку. Поле стало на 4 px выше комбобоксов Method/«Клиент»
// (py-1.5 text-sm, без рамки), ряд выравнивается по низу — и на вкладках
// «Логи» и «Метрики» разъезжались верхние края полей и метки над ними.
//
// Раскладки в jsdom нет, поэтому сверяются классы, задающие высоту бокса:
// фиксированная высота, вертикальные отступы, ширина рамки, размер шрифта.
// У поля поиска они обязаны совпадать с соседними комбобоксами.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { LogsAdvancedFilters } from "./LogsAdvancedFilters";
import { emptyAdvForm } from "../../lib/logsQuery";

// boxTokens — классы, от которых зависит внешняя высота поля. h-auto и
// border-0 нейтральны (снимают чужую высоту/рамку) и в сравнение не входят.
function boxTokens(el: HTMLElement): string[] {
  return el.className
    .split(/\s+/)
    .filter((c) => /^(h-(?!auto$)|py-|pt-|pb-|border(-[1-9][0-9]*)?$|text-(xs|sm|base|\[\d+px\])$)/.test(c))
    .sort();
}

function renderPanel(showDates: boolean) {
  apiGet.mockResolvedValue({ items: [] });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const clientWindow = { key: "24h", get: () => ({ from: 0, to: 1 }) };
  render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        {showDates ? (
          <LogsAdvancedFilters
            nodeId="n1"
            draft={emptyAdvForm}
            applied={emptyAdvForm}
            onDraft={() => {}}
            onCommit={() => {}}
          />
        ) : (
          <LogsAdvancedFilters
            nodeId="n1"
            draft={emptyAdvForm}
            applied={emptyAdvForm}
            onDraft={() => {}}
            onCommit={() => {}}
            showDates={false}
            clientWindow={clientWindow}
          />
        )}
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("LogsAdvancedFilters: поле поиска одной высоты с соседями", () => {
  it.each([
    ["журнал (с датами)", true],
    ["метрики (без дат)", false],
  ])("%s", (_name, showDates) => {
    renderPanel(showDates);
    const search = screen.getByPlaceholderText("logs.advanced.q_placeholder");
    const method = screen.getByText("logs.advanced.method_any").closest("button") as HTMLElement;
    const client = screen.getByText("logs.advanced.client_any").closest("button") as HTMLElement;

    expect(boxTokens(method)).toEqual(["py-1.5", "text-sm"]);
    expect(boxTokens(search)).toEqual(boxTokens(method));
    expect(boxTokens(client)).toEqual(boxTokens(method));
  });
});
