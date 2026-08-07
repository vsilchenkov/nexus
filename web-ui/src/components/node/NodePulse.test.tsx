import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { NodePulse } from "./NodePulse";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

// §84.6: понятия last seen в системе не было вовсе — узел, молчащий из-за
// обрыва, и узел, молчащий по расписанию, выглядели одинаково.

const NOW = Date.parse("2026-08-07T12:00:00.000Z");
const MIN = 60_000;

describe("NodePulse (§84.6)", () => {
  it("свежая активность — спокойный тон", () => {
    render(
      <NodePulse lastSeenMs={NOW - 4 * MIN} total={4558} stepSeconds={3600} available now={NOW} />,
    );
    expect(screen.getByText(/metrics\.pulse\.ago_m/)).toBeInTheDocument();
    expect(document.querySelector(".text-warn")).toBeNull();
  });

  // Порог тишины — ТРИ ШАГА ГРАФИКА: три пустых столбца подряд ровно то, что
  // видно глазом на соседнем графике, поэтому подпись и картинка не расходятся.
  it("молчание дольше трёх шагов графика красит предупреждением", () => {
    render(
      <NodePulse lastSeenMs={NOW - 200 * MIN} total={10} stepSeconds={3600} available now={NOW} />,
    );
    expect(document.querySelector(".text-warn")).not.toBeNull();
  });

  it("молчание короче трёх шагов предупреждением не красит", () => {
    render(
      <NodePulse lastSeenMs={NOW - 100 * MIN} total={10} stepSeconds={3600} available now={NOW} />,
    );
    expect(document.querySelector(".text-warn")).toBeNull();
  });

  it("в окне не было запросов — говорит об этом прямо, а не показывает эпоху", () => {
    render(<NodePulse lastSeenMs={0} total={0} stepSeconds={3600} available now={NOW} />);
    expect(screen.getByText("metrics.pulse.silent")).toBeInTheDocument();
    // Регресс на guard в NodeKPI: без него сюда приезжал 0 и рисовалось
    // «56 лет назад».
    expect(screen.queryByText(/ago_d/)).not.toBeInTheDocument();
  });

  it("источник недоступен — прочерк, а не «запросов не было»: это разные вещи", () => {
    render(<NodePulse lastSeenMs={0} total={0} stepSeconds={3600} available={false} now={NOW} />);
    expect(screen.queryByText("metrics.pulse.silent")).not.toBeInTheDocument();
    expect(screen.getByText(/metrics\.pulse\.label/)).toBeInTheDocument();
  });

  // Полный max по времени без окна — скан колонки на всей таблице (боевая
  // внешняя §64 — 10,2 млн записей), а вкладка поллится каждые ~12 с.
  it("«за всё время» — только по клику, само не срабатывает", () => {
    const onShowAllTime = vi.fn();
    render(
      <NodePulse
        lastSeenMs={0}
        total={0}
        stepSeconds={3600}
        available
        now={NOW}
        onShowAllTime={onShowAllTime}
      />,
    );
    expect(onShowAllTime).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText("metrics.pulse.all_time"));
    expect(onShowAllTime).toHaveBeenCalledTimes(1);
  });

  it("без обработчика кнопки «за всё время» нет вовсе", () => {
    render(<NodePulse lastSeenMs={0} total={0} stepSeconds={3600} available now={NOW} />);
    expect(screen.queryByText("metrics.pulse.all_time")).not.toBeInTheDocument();
  });

  // Ревизия §84.10: проп onShowAllTime был, а вкладка его не передавала —
  // обещанная ТЗ §84.6 кнопка не появлялась НИКОГДА. Здесь закреплён показ
  // результата после клика.
  it("после загрузки показывается значение «за всё время», а не кнопка", () => {
    render(
      <NodePulse
        lastSeenMs={0}
        total={0}
        stepSeconds={3600}
        available
        now={NOW}
        allTimeMs={NOW - 3 * 24 * 60 * MIN}
        onShowAllTime={vi.fn()}
      />,
    );
    expect(screen.queryByText("metrics.pulse.all_time")).not.toBeInTheDocument();
    expect(screen.getByText(/metrics\.pulse\.all_time_value/)).toBeInTheDocument();
  });

  it("записей нет вовсе → так и сказано, а не «56 лет назад»", () => {
    render(
      <NodePulse
        lastSeenMs={0}
        total={0}
        stepSeconds={3600}
        available
        now={NOW}
        allTimeMs={0}
        onShowAllTime={vi.fn()}
      />,
    );
    expect(screen.getByText("metrics.pulse.all_time_none")).toBeInTheDocument();
  });
});
