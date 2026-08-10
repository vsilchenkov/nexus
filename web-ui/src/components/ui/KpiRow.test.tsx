import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

import { Kpi, KpiRow } from "./data";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

// Часть плиток условна: у sync-узла нет «Ожидают отправки» — очереди не
// существует (§69.1). Когда остаётся одна, двухколоночная сетка отдавала ей
// ровно половину строки, а вторую половину оставляла пустой — на вкладке
// «Очередь» это читалось как поломка вёрстки.

function grid(container: HTMLElement): HTMLElement | null {
  return container.querySelector<HTMLElement>(".grid");
}

describe("KpiRow", () => {
  it("две плитки и больше — обычная сетка", () => {
    const { container } = render(
      <KpiRow cols={2}>
        <Kpi label="a" value="1" />
        <Kpi label="b" value="2" />
      </KpiRow>,
    );
    expect(grid(container)).not.toBeNull();
    expect(grid(container)!.className).toContain("sm:grid-cols-2");
  });

  it("одна плитка сеткой НЕ выкладывается — иначе рядом дыра в пол-строки", () => {
    const { container } = render(
      <KpiRow cols={2}>
        <Kpi label="failed" value="0" />
      </KpiRow>,
    );
    expect(grid(container)).toBeNull();
    expect(container.firstElementChild!.className).toContain("max-w-xs");
  });

  // Условная плитка `{flag && <Kpi/>}` кладёт в children `false`. Наивный
  // Children.count посчитал бы его за элемент, и правило не сработало бы
  // именно там, ради чего писалось, — у sync-узла.
  it("выключенная условная плитка не считается за плитку", () => {
    const isAsync = false;
    const { container } = render(
      <KpiRow cols={2}>
        {isAsync && <Kpi label="pending" value="0" />}
        <Kpi label="failed" value="0" />
      </KpiRow>,
    );
    expect(grid(container)).toBeNull();
    expect(container.firstElementChild!.className).toContain("max-w-xs");
  });

  it("обе условные плитки на месте — сетка", () => {
    const isAsync = true;
    const { container } = render(
      <KpiRow cols={2}>
        {isAsync && <Kpi label="pending" value="0" />}
        <Kpi label="failed" value="0" />
      </KpiRow>,
    );
    expect(grid(container)).not.toBeNull();
  });
});
