import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import { PeriodPicker } from "./PeriodPicker";
import { resolveCustomPeriod, type Period } from "../../lib/period";

// i18n в тесте не поднимаем: t() возвращает ключ — проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

const custom = (from: string, to: string): Period => ({ kind: "custom", from, to });

// dateFields — кнопки-поля даты (§84.4: нативные datetime-local заменены
// календарём §48.8, поэтому ищем именно кнопки с меткой значения).
function dateFields(): HTMLElement[] {
  return screen
    .queryAllByRole("button")
    .filter((b) => b.querySelector("svg.lucide-calendar") !== null);
}

// fieldLabel — что показано в поле: «01.07.2026 13:30» либо ключ-плейсхолдер.
function fieldLabel(el: HTMLElement): string {
  return el.textContent?.trim() ?? "";
}

// dmy — ожидаемая метка поля в зоне рантайма теста.
function dmy(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getDate())}.${pad(d.getMonth() + 1)}.${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const HOUR = 3600_000;
const DAY = 24 * HOUR;

describe("resolveCustomPeriod (§84.4)", () => {
  const now = Date.parse("2026-08-07T12:00:00.000Z");
  const lookback = 60 * DAY;

  it("обе границы заданы — берутся как есть", () => {
    const r = resolveCustomPeriod("2026-08-05T00:00", "2026-08-06T00:00", lookback, now);
    expect(Date.parse(r!.to) - Date.parse(r!.from)).toBe(DAY);
  });

  it("пустое «по» считается до сейчас", () => {
    const r = resolveCustomPeriod("2026-08-07T10:00", "", lookback, now);
    expect(Date.parse(r!.to)).toBe(now);
  });

  it("пустое «от» считается от глубины хранения", () => {
    const r = resolveCustomPeriod("", "2026-08-07T12:00", lookback, now);
    const to = Date.parse(r!.to);
    expect(to - Date.parse(r!.from)).toBe(lookback);
  });

  it("обе пустые не применяются — один клик не запускает полный скан", () => {
    expect(resolveCustomPeriod("", "", lookback, now)).toBeNull();
  });

  it("перевёрнутый и вырожденный диапазон не применяется", () => {
    expect(resolveCustomPeriod("2026-08-06T00:00", "2026-08-05T00:00", lookback, now)).toBeNull();
    expect(resolveCustomPeriod("2026-08-05T00:00", "2026-08-05T00:00", lookback, now)).toBeNull();
  });

  it("мусор не применяется", () => {
    expect(resolveCustomPeriod("не дата", "2026-08-05T00:00", lookback, now)).toBeNull();
    expect(resolveCustomPeriod("2026-08-05T00:00", "не дата", lookback, now)).toBeNull();
  });

  it("нижняя граница всегда конкретна — «без нижней границы» наружу не уходит", () => {
    const r = resolveCustomPeriod("", "", lookback, now);
    expect(r).toBeNull();
    const one = resolveCustomPeriod("", "2026-08-07T12:00", lookback, now)!;
    expect(Number.isFinite(Date.parse(one.from))).toBe(true);
  });
});

describe("PeriodPicker (§84.4)", () => {
  it("у пресета календаря нет", () => {
    render(<PeriodPicker value={{ kind: "preset", range: "24h" }} onChange={vi.fn()} />);
    expect(dateFields()).toHaveLength(0);
  });

  // §84.4: кнопки «Применить» больше нет — лишний клик на каждое изменение.
  it("кнопки «Применить» нет — красный на старом коде", () => {
    render(<PeriodPicker value={custom("2026-07-01T10:30:00Z", "2026-07-02T11:45:00Z")} onChange={vi.fn()} />);
    expect(screen.queryByText("common.apply")).not.toBeInTheDocument();
  });

  // §54: восстановление фильтров отдаёт готовый произвольный период — он должен
  // быть ВИДЕН в полях, а не только применён к метрикам.
  it("поля засеваются из произвольного периода при монтировании", () => {
    render(
      <PeriodPicker value={custom("2026-07-01T10:30:00Z", "2026-07-02T11:45:00Z")} onChange={vi.fn()} />,
    );
    const [from, to] = dateFields();
    expect(fieldLabel(from)).toBe(dmy("2026-07-01T10:30:00Z"));
    expect(fieldLabel(to)).toBe(dmy("2026-07-02T11:45:00Z"));
  });

  // Клик «Узлы» на активной странице восстанавливает фильтры БЕЗ ремоунта —
  // период приезжает пропсом уже после монтирования.
  it("поля синхронизируются, когда период приходит после монтирования", () => {
    const { rerender } = render(
      <PeriodPicker value={{ kind: "preset", range: "1h" }} onChange={vi.fn()} />,
    );
    expect(dateFields()).toHaveLength(0);

    rerender(
      <PeriodPicker value={custom("2026-07-05T08:00:00Z", "2026-07-06T09:15:00Z")} onChange={vi.fn()} />,
    );
    const [from, to] = dateFields();
    expect(fieldLabel(from)).toBe(dmy("2026-07-05T08:00:00Z"));
    expect(fieldLabel(to)).toBe(dmy("2026-07-06T09:15:00Z"));
  });

  it("битый произвольный период не роняет компонент", () => {
    render(<PeriodPicker value={custom("junk", "also-junk")} onChange={vi.fn()} />);
    expect(screen.getByText("metrics.range.custom")).toBeInTheDocument();
    // Оба поля пустые → период не применён, и подпись это объясняет.
    expect(screen.getByText("metrics.range.need_one_bound")).toBeInTheDocument();
  });

  it("фактическое окно показано подписью, когда границы разрешимы", () => {
    render(<PeriodPicker value={custom("2026-07-01T10:00:00Z", "2026-07-02T10:00:00Z")} onChange={vi.fn()} />);
    expect(screen.queryByText("metrics.range.need_one_bound")).not.toBeInTheDocument();
    expect(screen.getByText(/↳ .* — .*/)).toBeInTheDocument();
  });

  // Главный жест §84.4: клик по дню применяет период сразу и закрывает
  // календарь. На старом коде здесь требовалось ещё нажать «Применить».
  it("клик по дню применяет период без отдельной кнопки", () => {
    const onChange = vi.fn();
    // Окно берём широким намеренно: на узком клик по дню, совпавшему с «по»,
    // даёт вырожденный диапазон, и resolveCustom честно ничего не применяет —
    // проверять надо основной жест, а не эту защиту (она закрыта выше).
    render(
      <PeriodPicker
        value={custom("2026-07-01T09:00:00Z", "2026-07-20T09:00:00Z")}
        onChange={onChange}
        maxLookbackMs={30 * DAY}
      />,
    );
    fireEvent.click(dateFields()[0]);

    // Календарь открылся: в поповере есть сетка дней. Берём ВТОРОЙ доступный:
    // первое число уже выбрано, а повторный клик по выбранному дню в режиме
    // single снимает выбор и ничего не применяет.
    const grid = screen.getByRole("grid");
    const enabled = within(grid)
      .getAllByRole("button")
      .filter((b) => !b.hasAttribute("disabled"));
    expect(enabled.length).toBeGreaterThan(1);
    fireEvent.click(enabled[1]);

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0].kind).toBe("custom");
    // Поповер закрылся — сетки больше нет.
    expect(screen.queryByRole("grid")).not.toBeInTheDocument();
  });
});
