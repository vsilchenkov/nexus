import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { MemoryRouter } from "react-router-dom";

import { MethodsChart, METHOD_ROWS_DEFAULT, METHOD_ROWS_STEP, type MethodStat } from "./MethodsChart";

// §102: рейтинг «Запросы по методам».
//
// Сторожим то, что видит пользователь и не видит типизация: природу элемента
// (ссылка, а не кнопка с обработчиком — урок §79.3/§100.2), сокращение имени в
// СЕРЕДИНЕ и арифметику строки «Остальные», которую сервер не присылает.

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) =>
      o ? `${k}:${Object.values(o).join(",")}` : k,
    i18n: { language: "ru" },
  }),
}));

const m = (method: string, total: number, delivered = total): MethodStat => ({
  method,
  total,
  delivered,
});

function renderChart(props: Partial<React.ComponentProps<typeof MethodsChart>> = {}) {
  const data = props.data ?? [m("v1/a", 10), m("v1/b", 5)];
  const total = props.total ?? data.reduce((s, x) => s + x.total, 0);
  const delivered = props.delivered ?? data.reduce((s, x) => s + x.delivered, 0);
  return render(
    <MemoryRouter>
      <TooltipProvider>
        <MethodsChart
          {...props}
          data={data}
          total={total}
          delivered={delivered}
          // Именно "in": props.hrefFor ?? default затёр бы явный undefined, и
          // случай «строк-ссылок быть не должно» проверялся бы со ссылками.
          hrefFor={
            "hrefFor" in props ? props.hrefFor : (method) => `/nodes/n1?tab=logs&method=${method}`
          }
        />
      </TooltipProvider>
    </MemoryRouter>,
  );
}

const LONG = "suggestions/api/4_1/rs/suggest/address/with/a/very/long/tail/value";

describe("MethodsChart: природа строки (§100.2)", () => {
  it("строка метода — настоящая ссылка с адресом, а не кнопка", () => {
    renderChart({ data: [m("v1/suggest", 10)] });
    const link = screen.getByRole("link");
    // getAttribute, а не .href: важен АТРИБУТ — именно он даёт контекстное меню
    // «Открыть в новой вкладке», Ctrl+клик и среднюю кнопку.
    expect(link.getAttribute("href")).toBe("/nodes/n1?tab=logs&method=v1/suggest");
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("строка с пустым методом ссылкой не является", () => {
    // Пустое значение фильтра в журнал не попадает: такая ссылка открыла бы ВСЕ
    // записи под видом «записей без подпути».
    renderChart({ data: [m("", 7)] });
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("metrics.methods.empty_method")).toBeTruthy();
  });

  it("без hrefFor строк-ссылок нет вовсе", () => {
    renderChart({ data: [m("v1/a", 3)], hrefFor: undefined });
    expect(screen.queryByRole("link")).toBeNull();
  });
});

describe("MethodsChart: сокращение длинного имени (§102.5)", () => {
  it("режет середину, сохраняя различающий хвост", () => {
    renderChart({ data: [m(LONG, 10)] });
    const shown = screen.getByRole("link").textContent ?? "";
    expect(shown).toContain("…");
    expect(shown).not.toContain(LONG);
    // Начало и хвост на месте — по ним строка и опознаётся.
    expect(shown.startsWith("suggestions")).toBe(true);
    expect(shown).toContain("tail/value");
  });

  it("два метода, отличающиеся только хвостом, остаются различимыми", () => {
    renderChart({
      data: [m(`${LONG}/address`, 10), m(`${LONG}/party`, 9)],
    });
    const [a, b] = screen.getAllByRole("link").map((el) => el.textContent ?? "");
    expect(a).not.toBe(b);
  });
});

describe("MethodsChart: строка «Остальные» (§102.3)", () => {
  const many: MethodStat[] = Array.from({ length: 12 }, (_, i) => m(`v1/m${i}`, 100 - i));

  it("сворачивает хвост и считает остаток как «Всего» − Σ(показанных)", () => {
    const total = many.reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    const shownSum = many.slice(0, METHOD_ROWS_DEFAULT).reduce((s, x) => s + x.total, 0);
    expect(screen.getByText("metrics.methods.others")).toBeTruthy();
    // 12 методов, показано 8 → в свёртке 4.
    expect(screen.getByText("· metrics.methods.others_n:4")).toBeTruthy();
    expect(screen.getByText(String(total - shownSum))).toBeTruthy();
  });

  it("не рисует нулевую или отрицательную свёртку", () => {
    // Σ по методам БОЛЬШЕ «Всего» — так выглядит внешняя таблица §64 и
    // приблизительный счёт. Нулевая строка рядом с «есть ещё методы» была бы
    // прямой ложью на экране.
    const total = many.slice(0, METHOD_ROWS_DEFAULT).reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    expect(screen.queryByText("metrics.methods.others")).toBeNull();
  });

  it("при усечении на сервере счётчик свёртки читается как «более N»", () => {
    const total = many.reduce((s, x) => s + x.total, 0) + 500;
    renderChart({ data: many, total, delivered: total, truncated: true });
    expect(screen.getByText("· metrics.methods.others_n_more:4")).toBeTruthy();
  });

  it("последний оставшийся метод показывается сам, а не «Остальные · 1»", () => {
    // §102.2: свёртка одного элемента прячет имя и ничего не экономит.
    const nine = Array.from({ length: METHOD_ROWS_DEFAULT + 1 }, (_, i) => m(`v1/m${i}`, 100 - i));
    const total = nine.reduce((s, x) => s + x.total, 0);
    renderChart({ data: nine, total, delivered: total });
    expect(screen.getAllByRole("link")).toHaveLength(nine.length);
    expect(screen.queryByText("metrics.methods.others")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("но при усечении на сервере девятая строка сворачивается: за ней неизвестный хвост", () => {
    const nine = Array.from({ length: METHOD_ROWS_DEFAULT + 1 }, (_, i) => m(`v1/m${i}`, 100 - i));
    const total = nine.reduce((s, x) => s + x.total, 0) + 300;
    renderChart({ data: nine, total, delivered: total, truncated: true });
    expect(screen.getAllByRole("link")).toHaveLength(METHOD_ROWS_DEFAULT);
    expect(screen.getByText("metrics.methods.others")).toBeTruthy();
  });

  it("свёртка в журнал не ведёт", () => {
    const total = many.reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    // Ссылок ровно столько, сколько показанных методов: свёртка не ссылка.
    expect(screen.getAllByRole("link")).toHaveLength(METHOD_ROWS_DEFAULT);
  });
});

describe("MethodsChart: «Показать ещё» (§102.5)", () => {
  const many: MethodStat[] = Array.from({ length: 12 }, (_, i) => m(`v1/m${i}`, 100 - i));

  it("до раскрытия видно восемь строк, после — ещё восемь (здесь это весь список)", () => {
    const total = many.reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    expect(screen.getAllByRole("link")).toHaveLength(METHOD_ROWS_DEFAULT);

    fireEvent.click(screen.getByRole("button", { name: "metrics.methods.show_more" }));
    expect(screen.getAllByRole("link")).toHaveLength(many.length);
    // Раскрывать больше нечего — кнопка уходит.
    expect(screen.queryByRole("button", { name: "metrics.methods.show_more" })).toBeNull();
    // И свёртка пересчитана в ноль, а значит не рисуется.
    expect(screen.queryByText("metrics.methods.others")).toBeNull();
  });

  it("кнопки нет, когда методов не больше восьми", () => {
    renderChart({ data: many.slice(0, METHOD_ROWS_DEFAULT) });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("«Свернуть» возвращает рейтинг к восьми строкам и пересчитанной свёртке", () => {
    // Раскрытие обязано быть обратимым: длинный рейтинг вытесняет латентность и
    // ёмкость за край экрана, а другого способа вернуться, кроме перезагрузки
    // страницы, у пользователя нет.
    const total = many.reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    fireEvent.click(screen.getByRole("button", { name: "metrics.methods.show_more" }));
    expect(screen.getAllByRole("link")).toHaveLength(many.length);

    fireEvent.click(screen.getByRole("button", { name: "metrics.methods.show_less" }));
    expect(screen.getAllByRole("link")).toHaveLength(METHOD_ROWS_DEFAULT);
    expect(screen.getByText("metrics.methods.others")).toBeTruthy();
    expect(screen.getByRole("button", { name: "metrics.methods.show_more" })).toBeTruthy();
  });

  it("«Свернуть» не предлагается, когда сворачивать нечего", () => {
    // Девять методов: свёрнутый вид и так показывает все девять (последний
    // метод показывается сам), значит раскрывать и сворачивать нечего.
    const nine = Array.from({ length: METHOD_ROWS_DEFAULT + 1 }, (_, i) => m(`v1/m${i}`, 100 - i));
    renderChart({ data: nine });
    expect(screen.getAllByRole("link")).toHaveLength(nine.length);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("кнопка не уходит, пока есть непоказанные методы: список доводится до конца кликами", () => {
    // Раскрытие пошаговое, а не «сразу всё»: тридцать методов открываются
    // 8 → 16 → 24 → 30, и «Показать ещё» исчезает ровно тогда, когда
    // непоказанных не осталось. Иначе часть методов не увидеть вовсе.
    const thirty: MethodStat[] = Array.from({ length: 30 }, (_, i) => m(`v1/m${i}`, 100 - i));
    const total = thirty.reduce((s, x) => s + x.total, 0);
    renderChart({ data: thirty, total, delivered: total });

    const more = () => screen.queryByRole("button", { name: "metrics.methods.show_more" });
    const rows = () => screen.getAllByRole("link").length;

    expect(rows()).toBe(METHOD_ROWS_DEFAULT);
    for (const want of [
      METHOD_ROWS_DEFAULT + METHOD_ROWS_STEP,
      METHOD_ROWS_DEFAULT + 2 * METHOD_ROWS_STEP,
      thirty.length,
    ]) {
      const btn = more();
      expect(btn).toBeTruthy();
      fireEvent.click(btn!);
      expect(rows()).toBe(want);
    }
    // Непоказанных не осталось — кнопка сменилась на «Свернуть».
    expect(more()).toBeNull();
    expect(screen.getByRole("button", { name: "metrics.methods.show_less" })).toBeTruthy();
    // И свёртки «Остальные» больше нет: остаток ушёл в ноль.
    expect(screen.queryByText("metrics.methods.others")).toBeNull();
  });

  it("при усечении на сервере «Остальные» остаётся и после полного раскрытия", () => {
    // Сервер отдал свой максимум и сказал, что методов больше: раскрытие
    // доводит до конца ПРИСЛАННОГО списка, а хвост честно остаётся свёрткой.
    const ten: MethodStat[] = Array.from({ length: 10 }, (_, i) => m(`v1/m${i}`, 10));
    renderChart({ data: ten, total: 200, delivered: 200, truncated: true });
    fireEvent.click(screen.getByRole("button", { name: "metrics.methods.show_more" }));
    expect(screen.getAllByRole("link")).toHaveLength(ten.length);
    expect(screen.getByText("metrics.methods.others")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "metrics.methods.show_more" })).toBeNull();
  });

  it("кнопка стоит ВНЕ строк-ссылок", () => {
    const total = many.reduce((s, x) => s + x.total, 0);
    renderChart({ data: many, total, delivered: total });
    const btn = screen.getByRole("button", { name: "metrics.methods.show_more" });
    expect(btn.closest("a")).toBeNull();
  });
});

describe("MethodsChart: тултип (§102.5)", () => {
  // Тултип открывается и по фокусу — это тот же контент, что по наведению, но
  // в jsdom проверяемый без эмуляции указателя.
  async function openTooltip(el: HTMLElement) {
    await act(async () => {
      fireEvent.focus(el);
    });
  }

  it("показывает ПОЛНОЕ имя, сокращённое в строке", async () => {
    renderChart({ data: [m(LONG, 10, 9)] });
    const link = screen.getByRole("link");
    expect(link.textContent).not.toContain(LONG);

    await openTooltip(link);
    // Полное имя — в тултипе, и ровно один раз (строка его не повторяет).
    expect(screen.getAllByText(LONG).length).toBeGreaterThan(0);
  });

  it("несёт те же числа, что график «Запросы», и долю от всех", async () => {
    renderChart({ data: [m("v1/a", 10, 9), m("v1/b", 10, 10)], total: 20, delivered: 19 });
    await openTooltip(screen.getAllByRole("link")[0]);
    expect(screen.getAllByText("metrics.tooltip.delivered").length).toBeGreaterThan(0);
    expect(screen.getAllByText("metrics.tooltip.errors").length).toBeGreaterThan(0);
    // 10 из 20 — половина всех запросов.
    expect(screen.getAllByText("metrics.methods.tooltip.share:50").length).toBeGreaterThan(0);
    // И подсказка действия: строка ведёт в журнал.
    expect(screen.getAllByText("metrics.methods.open_logs").length).toBeGreaterThan(0);
  });

  it("у строки без подпути вместо действия — объяснение, почему она не ссылка", async () => {
    renderChart({ data: [m("", 5)] });
    await openTooltip(screen.getByText("metrics.methods.empty_method"));
    expect(screen.getAllByText("metrics.methods.empty_method_hint").length).toBeGreaterThan(0);
    expect(screen.queryByText("metrics.methods.open_logs")).toBeNull();
  });
});
