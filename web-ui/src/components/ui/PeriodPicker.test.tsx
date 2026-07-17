import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { PeriodPicker } from "./PeriodPicker";
import { type Period } from "../../lib/period";

// i18n в тесте не поднимаем: t() возвращает ключ — проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// local — RFC3339 → ожидаемое значение datetime-local в зоне рантайма теста
// (компонент показывает локальное время, поэтому ожидание считаем так же).
function local(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const custom = (from: string, to: string): Period => ({ kind: "custom", from, to });
const dateInputs = () =>
  Array.from(document.querySelectorAll<HTMLInputElement>('input[type="datetime-local"]'));

describe("PeriodPicker", () => {
  it("hides the calendar for a preset period", () => {
    render(<PeriodPicker value={{ kind: "preset", range: "24h" }} onChange={vi.fn()} />);
    expect(dateInputs()).toHaveLength(0);
  });

  // §54: восстановление фильтров отдаёт готовый произвольный период — он должен
  // быть ВИДЕН в полях, а не только применён к метрикам.
  it("seeds the calendar from a custom period on mount", () => {
    render(
      <PeriodPicker
        value={custom("2026-07-01T10:30:00Z", "2026-07-02T11:45:00Z")}
        onChange={vi.fn()}
      />,
    );
    const [from, to] = dateInputs();
    expect(from.value).toBe(local("2026-07-01T10:30:00Z"));
    expect(to.value).toBe(local("2026-07-02T11:45:00Z"));
  });

  // Клик «Узлы» на активной странице восстанавливает фильтры БЕЗ ремоунта —
  // период приезжает пропсом уже после монтирования.
  it("syncs the calendar when the custom period arrives after mount", () => {
    const { rerender } = render(
      <PeriodPicker value={{ kind: "preset", range: "1h" }} onChange={vi.fn()} />,
    );
    expect(dateInputs()).toHaveLength(0);

    rerender(
      <PeriodPicker
        value={custom("2026-07-05T08:00:00Z", "2026-07-06T09:15:00Z")}
        onChange={vi.fn()}
      />,
    );
    const [from, to] = dateInputs();
    expect(from.value).toBe(local("2026-07-05T08:00:00Z"));
    expect(to.value).toBe(local("2026-07-06T09:15:00Z"));
  });

  it("tolerates an unparsable custom period without crashing", () => {
    render(<PeriodPicker value={custom("junk", "also-junk")} onChange={vi.fn()} />);
    expect(screen.getByText("metrics.range.custom")).toBeInTheDocument();
    for (const i of dateInputs()) expect(i.value).toBe("");
  });
});
