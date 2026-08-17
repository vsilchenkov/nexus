import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ClickHousePanel } from "./ClickHouse";

// Регресс: Chrome считал пару «Пользователь» + «Пароль» формой логина и на
// открытии страницы вписывал в «Пользователь» сохранённый логин браузера
// (поля шли без autocomplete прямо перед type="password"). Тест фиксирует
// защитные атрибуты: off на текстовых полях, new-password на пароле.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: vi.fn(), post: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

// Соседние панели тянут собственные запросы — для этого теста они не нужны.
vi.mock("../../components/OrphanTablesPanel", () => ({ OrphanTablesPanel: () => null }));
vi.mock("../../components/CHTemplatesPanel", () => ({ CHTemplatesPanel: () => null }));

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.clearAllMocks();
  apiGet.mockResolvedValue({
    sentry: {},
    clickhouse: {
      host: "ch.example.com",
      port: 9000,
      database: "nexus",
      user: "writer",
      password: "***",
    },
    updated_at: "2026-08-17T10:00:00Z",
  });
});

describe("ClickHousePanel", () => {
  it("поля подключения защищены от автозаполнения браузера", async () => {
    render(<ClickHousePanel />, { wrapper });

    const user = await screen.findByDisplayValue("writer");
    expect(user).toHaveAttribute("autocomplete", "off");
    expect(screen.getByDisplayValue("ch.example.com")).toHaveAttribute("autocomplete", "off");
    expect(screen.getByDisplayValue("nexus")).toHaveAttribute("autocomplete", "off");

    const password = screen.getByDisplayValue("***");
    expect(password).toHaveAttribute("type", "password");
    expect(password).toHaveAttribute("autocomplete", "new-password");
  });
});
