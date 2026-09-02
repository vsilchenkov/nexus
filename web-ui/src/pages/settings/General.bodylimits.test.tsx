import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { GeneralPanel } from "./General";

// §97: рабочие лимиты размера тела в панели «Общие». Проверяется то, чего не
// видят ни типы, ни бэкенд-тесты: конверсия байт ↔ МиБ в обе стороны и то, что
// форма не даёт сохранить значение выше потолка сервера или async больше sync.
// Ошибка в конверсии дала бы лимит, отличающийся в миллион раз, и оба
// направления (сид формы и отправка) ломаются независимо.

const { apiGet, apiPut } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPut: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut, post: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    // Интерполяция потолка нужна в ассертах: подсказка обязана показывать
    // именно серверное значение, а не константу из кода формы.
    t: (k: string, o?: Record<string, unknown>) => (o?.max === undefined ? k : `${k}:${o.max}`),
    i18n: { language: "ru" },
  }),
}));

const MIB = 1024 * 1024;

function mockServer(opts: {
  maxBodyBytes?: number;
  maxAsyncBodyBytes?: number;
  syncCapBytes?: number;
  asyncCapBytes?: number;
}) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/settings/app") {
      return Promise.resolve({
        general: {
          public_base_url: "https://nexus.example.com",
          max_body_bytes: opts.maxBodyBytes,
          max_async_body_bytes: opts.maxAsyncBodyBytes,
        },
        updated_at: "2026-09-02T10:11:00Z",
      });
    }
    if (url === "/api/settings/public") {
      return Promise.resolve({
        max_body_bytes_cap: opts.syncCapBytes,
        max_async_body_bytes_cap: opts.asyncCapBytes,
      });
    }
    if (url === "/api/version") {
      return Promise.resolve({ version: "1.34.0", override_allowed: false });
    }
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

describe("GeneralPanel — лимиты размера тела (§97)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
  });

  it("показывает сохранённые лимиты в МиБ, а не в байтах", async () => {
    mockServer({
      maxBodyBytes: 150 * MIB,
      maxAsyncBodyBytes: 120 * MIB,
      syncCapBytes: 300 * MIB,
      asyncCapBytes: 300 * MIB,
    });
    render(<GeneralPanel />, { wrapper });

    expect(await screen.findByDisplayValue("150")).toBeTruthy();
    expect(screen.getByDisplayValue("120")).toBeTruthy();
  });

  it("подсказка показывает потолок сервера", async () => {
    mockServer({ syncCapBytes: 300 * MIB, asyncCapBytes: 200 * MIB });
    render(<GeneralPanel />, { wrapper });

    expect(await screen.findByText("settings.general.max_body_sync_hint:300")).toBeTruthy();
    expect(screen.getByText("settings.general.max_body_async_hint:200")).toBeTruthy();
  });

  it("отправляет введённые МиБ как байты", async () => {
    mockServer({
      maxBodyBytes: 100 * MIB,
      maxAsyncBodyBytes: 60 * MIB,
      syncCapBytes: 300 * MIB,
      asyncCapBytes: 300 * MIB,
    });
    render(<GeneralPanel />, { wrapper });

    const sync = await screen.findByDisplayValue("100");
    fireEvent.change(sync, { target: { value: "250" } });
    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    const body = apiPut.mock.calls[0][1] as { general: Record<string, number> };
    expect(body.general.max_body_bytes).toBe(250 * MIB);
  });

  it("блокирует сохранение при значении выше потолка сервера", async () => {
    mockServer({
      maxBodyBytes: 100 * MIB,
      maxAsyncBodyBytes: 60 * MIB,
      syncCapBytes: 300 * MIB,
      asyncCapBytes: 300 * MIB,
    });
    render(<GeneralPanel />, { wrapper });

    const sync = await screen.findByDisplayValue("100");
    fireEvent.change(sync, { target: { value: "400" } });

    expect(screen.getByText("settings.general.max_body_over_cap:300")).toBeTruthy();
    const save = screen.getByText("common.save") as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    fireEvent.click(save);
    expect(apiPut).not.toHaveBeenCalled();
  });

  it("блокирует сохранение, когда async больше sync", async () => {
    mockServer({
      maxBodyBytes: 100 * MIB,
      maxAsyncBodyBytes: 50 * MIB,
      syncCapBytes: 300 * MIB,
      asyncCapBytes: 300 * MIB,
    });
    render(<GeneralPanel />, { wrapper });

    const asyncField = await screen.findByDisplayValue("50");
    fireEvent.change(asyncField, { target: { value: "200" } });

    expect(screen.getByText("settings.general.max_body_async_over_sync")).toBeTruthy();
    expect((screen.getByText("common.save") as HTMLButtonElement).disabled).toBe(true);
  });

  it("пустое поле не отправляется — текущее значение остаётся нетронутым", async () => {
    mockServer({ syncCapBytes: 300 * MIB, asyncCapBytes: 300 * MIB });
    render(<GeneralPanel />, { wrapper });

    await screen.findByText("common.save");
    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    const body = apiPut.mock.calls[0][1] as { general: Record<string, unknown> };
    expect(body.general.max_body_bytes).toBeUndefined();
    expect(body.general.max_async_body_bytes).toBeUndefined();
  });
});
