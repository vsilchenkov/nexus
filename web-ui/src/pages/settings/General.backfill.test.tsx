import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { GeneralPanel } from "./General";

// §106.4: расписание дозаполнения client_host в панели «Общие». Пустая строка
// здесь — значение («выключено»), а не «не трогать», как у числовых полей.
// Поэтому проверяется то, чего не видят бэкенд-тесты: незаданное значение
// показывается действующим расписанием, очистка уходит в PUT пустой строкой,
// а нетронутое поле не уходит вовсе.

const { apiGet, apiPut } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPut: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut, post: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

const FIELD = "settings.general.client_host_backfill_cron";

function mockServer(cron?: string) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/settings/app") {
      return Promise.resolve({
        general: { public_base_url: "", client_host_backfill_cron: cron },
        updated_at: "2026-09-22T10:11:00Z",
      });
    }
    if (url === "/api/version") {
      return Promise.resolve({ version: "1.38.0", override_allowed: false });
    }
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

async function field(): Promise<HTMLInputElement> {
  return (await screen.findByLabelText(FIELD)) as HTMLInputElement;
}

async function savedGeneral(): Promise<Record<string, unknown>> {
  fireEvent.click(screen.getByText("common.save"));
  await waitFor(() => expect(apiPut).toHaveBeenCalled());
  return (apiPut.mock.calls[0][1] as { general: Record<string, unknown> }).general;
}

describe("GeneralPanel — расписание дозаполнения client_host (§106.4)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
  });

  it("незаданное значение показывает расписание по умолчанию", async () => {
    mockServer(undefined);
    render(<GeneralPanel />, { wrapper });
    await waitFor(async () => expect((await field()).value).toBe("0 * * * *"));
    expect(screen.queryByText(`${FIELD}_disabled_warn`)).toBeNull();
  });

  it("сохранённое выражение гидратирует поле", async () => {
    mockServer("*/10 * * * *");
    render(<GeneralPanel />, { wrapper });
    await waitFor(async () => expect((await field()).value).toBe("*/10 * * * *"));
  });

  it("нетронутое поле в PUT не уходит", async () => {
    mockServer("*/10 * * * *");
    render(<GeneralPanel />, { wrapper });
    await waitFor(async () => expect((await field()).value).toBe("*/10 * * * *"));

    const general = await savedGeneral();
    expect(general).not.toHaveProperty("client_host_backfill_cron");
  });

  it("изменённое выражение уходит в PUT", async () => {
    mockServer(undefined);
    render(<GeneralPanel />, { wrapper });
    const input = await field();
    await waitFor(() => expect(input.value).toBe("0 * * * *"));
    fireEvent.change(input, { target: { value: " 30 2 * * * " } });

    const general = await savedGeneral();
    expect(general.client_host_backfill_cron).toBe("30 2 * * *");
  });

  it("очистка уходит пустой строкой и предупреждает о выключении", async () => {
    mockServer("0 * * * *");
    render(<GeneralPanel />, { wrapper });
    const input = await field();
    await waitFor(() => expect(input.value).toBe("0 * * * *"));
    fireEvent.change(input, { target: { value: "" } });
    expect(screen.getByText(`${FIELD}_disabled_warn`)).toBeTruthy();

    const general = await savedGeneral();
    expect(general.client_host_backfill_cron).toBe("");
  });

  it("выключенная задача показывает пустое поле", async () => {
    mockServer("");
    render(<GeneralPanel />, { wrapper });
    // Пустое значение гидратируется как есть, а не подменяется дефолтом.
    await screen.findByText(`${FIELD}_disabled_warn`);
    expect((await field()).value).toBe("");
  });
});
