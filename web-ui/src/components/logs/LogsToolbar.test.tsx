import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { LogsToolbar } from "./LogsToolbar";

// i18n в тесте не поднимаем: t() возвращает ключ — проверяем по ключам.
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function renderToolbar(over: Partial<Parameters<typeof LogsToolbar>[0]> = {}) {
  const props = {
    services: [] as never[],
    onServicesChange: vi.fn(),
    level: "info" as const,
    onLevelChange: vi.fn(),
    query: "",
    onQueryChange: vi.fn(),
    live: true,
    onLiveToggle: vi.fn(),
    limit: 200,
    onLimitChange: vi.fn(),
    onRefresh: vi.fn(),
    downloadHref: "/api/logs/download?limit=200",
    ...over,
  };
  render(<LogsToolbar {...props} />);
  return props;
}

describe("LogsToolbar", () => {
  it("chip click narrows service filter", () => {
    const p = renderToolbar();
    fireEvent.click(screen.getByRole("button", { name: "sender" }));
    expect(p.onServicesChange).toHaveBeenCalledWith(["sender"]);
  });

  it("'all' chip resets service filter", () => {
    const p = renderToolbar({ services: ["sender"] as never[] });
    fireEvent.click(screen.getByRole("button", { name: "logs.viewer.all_services" }));
    expect(p.onServicesChange).toHaveBeenCalledWith([]);
  });

  it("level segment triggers the REAL level change (side effect, not just a view filter)", () => {
    const p = renderToolbar();
    fireEvent.click(screen.getByRole("button", { name: "logs.viewer.level.warn" }));
    expect(p.onLevelChange).toHaveBeenCalledWith("warn");
  });

  it("search input propagates query", () => {
    const p = renderToolbar();
    fireEvent.change(screen.getByLabelText("logs.viewer.search_placeholder"), {
      target: { value: "redirect" },
    });
    expect(p.onQueryChange).toHaveBeenCalledWith("redirect");
  });

  it("live toggle and refresh fire callbacks", () => {
    const p = renderToolbar();
    fireEvent.click(screen.getByRole("button", { name: /logs\.viewer\.live/ }));
    expect(p.onLiveToggle).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "logs.viewer.refresh" }));
    expect(p.onRefresh).toHaveBeenCalledTimes(1);
  });

  it("download link points to the API href", () => {
    renderToolbar({ downloadHref: "/api/logs/download?limit=500&service=sender" });
    const link = screen.getByText("logs.viewer.download").closest("a");
    expect(link).toHaveAttribute("href", "/api/logs/download?limit=500&service=sender");
    expect(link).toHaveAttribute("download");
  });

  it("limit select calls back with a number", () => {
    const p = renderToolbar();
    fireEvent.change(screen.getByLabelText("logs.viewer.limit"), { target: { value: "1000" } });
    expect(p.onLimitChange).toHaveBeenCalledWith(1000);
  });
});
