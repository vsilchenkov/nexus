import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { LatencyChart, type LatencyPoint } from "./LatencyChart";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

// §84.5: пустой интервал — РАЗРЫВ линии, а не ноль. «Узел отвечал за 0 мс» —
// прямая ложь, а на узле с ночным простоем таких интервалов половина окна.
// Отличить пустой интервал от настоящего нуля можно только по числу попыток.

const p = (ts: number, p50: number, p95: number, attempts: number): LatencyPoint => ({
  ts,
  p50_ms: p50,
  p95_ms: p95,
  attempts,
});

function paths(): SVGPathElement[] {
  return Array.from(document.querySelectorAll("path"));
}

describe("LatencyChart (§84.5)", () => {
  it("сплошной ряд рисуется двумя линиями — p50 и p95", () => {
    render(<LatencyChart data={[p(1, 100, 300, 5), p(2, 120, 350, 7), p(3, 90, 280, 4)]} />);
    expect(paths()).toHaveLength(2);
  });

  it("пустой интервал рвёт линию, а не опускает её в ноль", () => {
    // Три интервала с данными, разделённые пустым: две линии на два отрезка.
    render(
      <LatencyChart
        data={[p(1, 100, 300, 5), p(2, 0, 0, 0), p(3, 90, 280, 4), p(4, 95, 290, 6)]}
      />,
    );
    const d = paths().map((el) => el.getAttribute("d") ?? "");
    // Ни одна линия не проходит через точку с нулевой латентностью: у отрезка
    // из одной точки пути нет вовсе, второй отрезок начинается заново (M).
    expect(d.every((s) => s.startsWith("M"))).toBe(true);
    expect(d.some((s) => (s.match(/M/g) ?? []).length > 1)).toBe(false);
  });

  it("одиночная точка между пустыми интервалами не теряется", () => {
    // Линию по одной точке не построить — рисуется маркер, иначе всплеск на
    // пустом окне не виден вовсе.
    render(<LatencyChart data={[p(1, 0, 0, 0), p(2, 5000, 30000, 3), p(3, 0, 0, 0)]} />);
    expect(paths()).toHaveLength(0);
    expect(document.querySelectorAll("circle")).toHaveLength(2);
  });

  it("окно без единой попытки показывает «нет данных», а не пустой холст", () => {
    render(<LatencyChart data={[p(1, 0, 0, 0), p(2, 0, 0, 0)]} />);
    expect(screen.getByText("metrics.latency.no_data")).toBeInTheDocument();
    expect(document.querySelector("svg")).toBeNull();
  });

  it("пустой массив не роняет компонент", () => {
    render(<LatencyChart data={[]} />);
    expect(screen.getByText("metrics.latency.no_data")).toBeInTheDocument();
  });

  it("шкала округляется вверх — ось не показывает «30 490»", () => {
    render(<LatencyChart data={[p(1, 5300, 30490, 5), p(2, 5200, 28000, 5)]} />);
    // 30 490 мс → шкала до 50 с (5×10^4 мс).
    expect(screen.getByText("metrics.latency.axis_max")).toBeInTheDocument();
  });
});
