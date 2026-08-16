import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MailPanel } from "./Mail";

// §88.8.4: панель почтовых настроек. Проверяется то, чего не видят ни типы, ни
// бэкенд-тесты: что маска пароля уходит на сервер как есть (и потому не
// затирает сохранённый), что тест шлёт НЕСОХРАНЁННОЕ состояние формы вместе с
// получателем, и что предупреждение о публичном адресе показывается по факту
// его отсутствия.

const { apiGet, apiPut, apiPost } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPut: vi.fn(),
  apiPost: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, put: apiPut, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

type MailSettings = Record<string, unknown>;

function mockServer(mail: MailSettings, publicBaseURL = "https://nexus.example.com") {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u1", role: "admin", email: "admin@example.com" } });
    }
    if (url === "/api/settings/app") {
      return Promise.resolve({
        mail,
        general: { public_base_url: publicBaseURL },
        updated_at: "2026-08-12T10:11:00Z",
      });
    }
    return Promise.resolve({});
  });
  apiPut.mockResolvedValue({});
  apiPost.mockResolvedValue({ ok: true, latency_ms: 842 });
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const configured: MailSettings = {
  enabled: true,
  host: "smtp.example.com",
  port: 587,
  encryption: "starttls",
  auth_type: "plain",
  username: "noreply@example.com",
  password: "***", // сервер всегда отдаёт маску
  from_address: "nexus@example.com",
  from_name: "Nexus",
  password_reset_enabled: true,
  password_reset_ttl_min: 60,
};

beforeEach(() => {
  vi.clearAllMocks();
});

describe("MailPanel", () => {
  it("заполняет форму сохранёнными настройками", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });

    expect(await screen.findByDisplayValue("smtp.example.com")).toBeInTheDocument();
    expect(screen.getByDisplayValue("587")).toBeInTheDocument();
    expect(screen.getByDisplayValue("nexus@example.com")).toBeInTheDocument();
    expect(screen.getByDisplayValue("60")).toBeInTheDocument();
  });

  // Маску нельзя «улучшать» на клиенте: сервер отличает "***" от нового
  // значения и только по ней понимает, что пароль менять не нужно.
  it("отправляет маску пароля без изменений, если её не трогали", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    await screen.findByDisplayValue("smtp.example.com");

    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    const [url, body] = apiPut.mock.calls[0];
    expect(url).toBe("/api/settings/app");
    expect(body.mail.password).toBe("***");
    expect(body.mail.host).toBe("smtp.example.com");
    expect(body.mail.port).toBe(587);
  });

  it("шлёт несохранённое состояние формы и получателя в проверку", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    const host = await screen.findByDisplayValue("smtp.example.com");

    fireEvent.change(host, { target: { value: "smtp2.example.com" } });
    fireEvent.click(screen.getByText("settings.mail.test_send"));

    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    const [url, body] = apiPost.mock.calls[0];
    expect(url).toBe("/api/settings/mail/test");
    expect(body.host).toBe("smtp2.example.com");
    expect(body.to).toBe("admin@example.com");
  });

  it("подставляет email текущего пользователя в поле «Кому»", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    expect(await screen.findByDisplayValue("admin@example.com")).toBeInTheDocument();
  });

  it("блокирует проверку при пустом получателе", async () => {
    mockServer(configured);
    apiGet.mockImplementation((url: string) => {
      if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role: "admin" } });
      if (url === "/api/settings/app") {
        return Promise.resolve({ mail: configured, general: {}, updated_at: "" });
      }
      return Promise.resolve({});
    });
    render(<MailPanel />, { wrapper });
    await screen.findByDisplayValue("smtp.example.com");

    expect(screen.getByText("settings.mail.test_send")).toBeDisabled();
  });

  // Пустое числовое поле означает «не задано», а не 0: иначе очищенный
  // таймаут уехал бы нулём и не прошёл валидацию на сервере.
  it("очищенное числовое поле уходит как undefined, а не 0", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    const port = await screen.findByDisplayValue("587");

    fireEvent.change(port, { target: { value: "" } });
    fireEvent.click(screen.getByText("common.save"));

    await waitFor(() => expect(apiPut).toHaveBeenCalled());
    expect(apiPut.mock.calls[0][1].mail.port).toBeUndefined();
  });

  it("показывает результат проверки", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    await screen.findByDisplayValue("smtp.example.com");

    fireEvent.click(screen.getByText("settings.mail.test_send"));
    expect(await screen.findByText("settings.common.test_ok")).toBeInTheDocument();
  });

  it("показывает ошибку проверки текстом сервера", async () => {
    mockServer(configured);
    apiPost.mockResolvedValue({ ok: false, error: "dial tcp: i/o timeout" });
    render(<MailPanel />, { wrapper });
    await screen.findByDisplayValue("smtp.example.com");

    fireEvent.click(screen.getByText("settings.mail.test_send"));
    expect(await screen.findByText("settings.common.test_failed")).toBeInTheDocument();
  });

  // §88.4.5: без публичного адреса ссылка в письме получится без хоста —
  // предупреждение обязано быть видно на этом же экране.
  it("предупреждает о незаданном публичном адресе", async () => {
    mockServer(configured, "");
    render(<MailPanel />, { wrapper });

    expect(await screen.findByText("settings.mail.base_url_missing")).toBeInTheDocument();
    expect(screen.queryByText("settings.mail.base_url_ok")).not.toBeInTheDocument();
  });

  it("не предупреждает, когда публичный адрес задан", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });

    expect(await screen.findByText("settings.mail.base_url_ok")).toBeInTheDocument();
    expect(screen.queryByText("settings.mail.base_url_missing")).not.toBeInTheDocument();
  });

  // Предупреждение о небезопасном режиме появляется только когда он включён —
  // иначе это фоновый шум, который перестают замечать.
  it("предупреждает об отключённой проверке сертификата только при включённом флаге", async () => {
    mockServer(configured);
    render(<MailPanel />, { wrapper });
    await screen.findByDisplayValue("smtp.example.com");

    expect(screen.queryByText("settings.mail.skip_tls_verify_hint")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("settings.mail.skip_tls_verify"));
    expect(await screen.findByText("settings.mail.skip_tls_verify_hint")).toBeInTheDocument();
  });
});
