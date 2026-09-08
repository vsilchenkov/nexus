import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { GeneralPanel } from "./General";

// §98.5: глобальная политика защиты узла в панели «Общие». Проверяется контракт
// пустого поля: пусто означает «действует конфигурация сервиса», а не ноль, и
// поле, которое оператор не трогал, не должно уезжать в запрос вовсе — иначе
// сохранение любой другой настройки затирало бы политику.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut, post: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string) => k,
    i18n: { language: "ru" },
  }),
}));

function mockServer(general: Record<string, unknown>) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/settings/app") {
      return Promise.resolve({ general, updated_at: "2026-09-07T10:11:00Z" });
    }
    if (url === "/api/settings/public") return Promise.resolve({});
    if (url === "/api/version") return Promise.resolve({ version: "1.34.1", override_allowed: false });
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe("GeneralPanel — политика защиты узла (§98.5)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
  });

  it("показывает сохранённые значения", async () => {
    mockServer({ circuit_breaker_threshold: 2, circuit_breaker_cooldown_sec: 5 });
    render(<GeneralPanel />, { wrapper });

    expect(await screen.findByDisplayValue("2")).toBeTruthy();
    expect(screen.getByDisplayValue("5")).toBeTruthy();
  });

  it("не задано (null) — поля пустые, а не нулевые", async () => {
    mockServer({ circuit_breaker_threshold: null, circuit_breaker_cooldown_sec: null });
    render(<GeneralPanel />, { wrapper });

    await waitFor(() => expect(screen.getByText("settings.general.breaker_threshold")).toBeTruthy());
    expect(screen.queryByDisplayValue("0")).toBeNull();
  });

  it("пустые поля не уезжают в запрос — сохранение соседней настройки не затирает политику", async () => {
    mockServer({ public_base_url: "https://nexus.example.com" });
    render(<GeneralPanel />, { wrapper });

    await waitFor(() => expect(screen.getByText("common.save")).toBeTruthy());
    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalledTimes(1));
    const body = apiPut.mock.calls[0][1] as { general: Record<string, unknown> };
    expect(body.general).not.toHaveProperty("circuit_breaker_threshold");
    expect(body.general).not.toHaveProperty("circuit_breaker_cooldown_sec");
  });

  it("введённые значения уходят числами", async () => {
    mockServer({ circuit_breaker_threshold: 5, circuit_breaker_cooldown_sec: 30 });
    render(<GeneralPanel />, { wrapper });

    const threshold = await screen.findByDisplayValue("5");
    fireEvent.change(threshold, { target: { value: "2" } });
    const cooldown = screen.getByDisplayValue("30");
    fireEvent.change(cooldown, { target: { value: "5" } });
    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalledTimes(1));
    const body = apiPut.mock.calls[0][1] as { general: Record<string, unknown> };
    expect(body.general.circuit_breaker_threshold).toBe(2);
    expect(body.general.circuit_breaker_cooldown_sec).toBe(5);
  });
});
