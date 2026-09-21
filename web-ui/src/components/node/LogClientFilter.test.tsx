import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogClientFilter, type ClientValue } from "./LogClientFilter";
import { type ClientWindow } from "../../lib/logsQuery";

// §105.4: фильтр «Клиент». Бой (§105.1): IP без PTR-записи в прежний фильтр
// «Хост клиента» не попадали вовсе — фасет отдавал только непустые имена. Здесь
// проверяется, что такой IP виден и выбирается, что выбор идёт по IP, а имя —
// подпись, и что числа подписаны окном, за которое посчитаны.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o?.n === undefined ? k : `${k}:${o.n}`),
  }),
}));

const NODE3 = "SRV-P-1C-NODE3.vz78.vozovoz.ru";

const FACET = {
  items: [
    { ip: "192.168.2.5", host: "", count: 61 },
    { ip: "192.168.86.246", host: NODE3, count: 39 },
    { ip: "", host: "gate-1.example", count: 5 },
    { ip: "", host: "", count: 2 },
  ],
  truncated: false,
  logs_configured: true,
  logs_available: true,
};

const WITH_FROM: ClientWindow = { key: "k", get: () => ({ from: 1000, to: 2000 }) };
const NO_WINDOW: ClientWindow = { key: "none", get: () => ({}) };

function renderFilter(value: ClientValue, facetWindow: ClientWindow = WITH_FROM) {
  const onChange = vi.fn();
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <LogClientFilter nodeId="n1" value={value} facetWindow={facetWindow} onChange={onChange} />
    </QueryClientProvider>,
  );
  return { onChange };
}

const ANY: ClientValue = { ip: "", host: "" };

function openAny() {
  fireEvent.click(screen.getByRole("button", { name: "logs.advanced.client_any" }));
}

const optionOf = (text: string) => screen.getByText(text).closest("[cmdk-item]") as HTMLElement;

describe("LogClientFilter (§105.4)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiGet.mockResolvedValue(FACET);
  });

  it("фасет грузится при открытии, с окном экрана", async () => {
    renderFilter(ANY);
    expect(apiGet).not.toHaveBeenCalled();
    openAny();
    await screen.findByText(NODE3);
    expect(apiGet).toHaveBeenCalledWith(
      "/api/nodes/n1/logs/clients",
      { from: "1000", to: "2000" },
      expect.objectContaining({ signal: expect.anything() }),
    );
  });

  it("IP без имени виден и выбирается по IP", async () => {
    const { onChange } = renderFilter(ANY);
    openAny();
    await screen.findByText("192.168.2.5");
    expect(optionOf("192.168.2.5")).toHaveTextContent("logs.advanced.client_unnamed");
    expect(optionOf("192.168.2.5")).toHaveTextContent("61");

    fireEvent.click(screen.getByText("192.168.2.5"));
    expect(onChange).toHaveBeenCalledWith({ ip: "192.168.2.5", host: "" });
  });

  it("у значения с именем выбор тоже по IP — имя только подпись", async () => {
    const { onChange } = renderFilter(ANY);
    openAny();
    fireEvent.click(await screen.findByText(NODE3));
    expect(onChange).toHaveBeenCalledWith({ ip: "192.168.86.246", host: "" });
  });

  it("значение без IP (внешняя таблица §64) выбирается по имени", async () => {
    const { onChange } = renderFilter(ANY);
    openAny();
    fireEvent.click(await screen.findByText("gate-1.example"));
    expect(onChange).toHaveBeenCalledWith({ ip: "", host: "gate-1.example" });
  });

  it("без IP и без имени — пункт неактивен, причина написана в строке", async () => {
    const { onChange } = renderFilter(ANY);
    openAny();
    await screen.findByText("logs.advanced.client_unknown");
    const row = optionOf("logs.advanced.client_unknown");
    expect(row).toHaveAttribute("aria-disabled", "true");
    expect(row).toHaveTextContent("logs.advanced.client_unknown_tip");
    fireEvent.click(row);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("поле показывает IP, пока фасет не загружен, и имя — после", async () => {
    renderFilter({ ip: "192.168.86.246", host: "" });
    const trigger = screen.getByRole("button", { name: /192\.168\.86\.246/ });
    fireEvent.click(trigger);
    await screen.findByText("39");
    await waitFor(() => expect(trigger).toHaveTextContent(NODE3));
  });

  it.each([
    { name: "с началом периода", w: WITH_FROM, want: "logs.advanced.client_window_period" },
    { name: "без периода", w: NO_WINDOW, want: "logs.advanced.client_window_last_day" },
  ])("подпись окна: $name", async ({ w, want }) => {
    renderFilter(ANY, w);
    openAny();
    expect(await screen.findByText(want)).toBeInTheDocument();
  });

  it("без периода параметры окна не уходят — сервер берёт сутки", async () => {
    renderFilter(ANY, NO_WINDOW);
    openAny();
    await screen.findByText(NODE3);
    expect(apiGet.mock.calls[0][1]).toEqual({});
  });

  it("усечённый список говорит об этом", async () => {
    apiGet.mockResolvedValue({ ...FACET, truncated: true });
    renderFilter(ANY);
    openAny();
    expect(await screen.findByText("logs.advanced.client_truncated:4")).toBeInTheDocument();
  });

  it("ClickHouse недоступен — подпись говорит об этом, а не «0 записей»", async () => {
    apiGet.mockResolvedValue({ items: [], logs_configured: true, logs_available: false });
    renderFilter(ANY);
    openAny();
    expect(await screen.findByText("logs.advanced.client_unavailable")).toBeInTheDocument();
  });

  it("сброс крестиком — «Любой»", () => {
    const { onChange } = renderFilter({ ip: "10.0.0.1", host: "" });
    const trigger = screen.getByRole("button", { name: /10\.0\.0\.1/ });
    fireEvent.click(trigger.querySelector("svg") as Element);
    expect(onChange).toHaveBeenCalledWith(ANY);
  });
});
