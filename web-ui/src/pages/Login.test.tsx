import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Login from "./Login";

// §85.8: логин `admin` подставляется ТОЛЬКО на стенде.
//
// Регресс, красный на прежнем коде: поле инициализировалось литералом "admin"
// безусловно, и боевая форма входа называла имя существующего
// привилегированного аккаунта каждому, кто открыл страницу.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function renderLogin() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<Login />, { wrapper });
}

// Поле логина — единственный textbox формы: у password-инпута этой роли нет.
// По подписи не ищем: атом Field не связывает label с input (нет for/id), и
// тест ловил бы не поведение, а чужой недочёт разметки.
function loginField(): HTMLInputElement {
  return screen.getByRole("textbox") as HTMLInputElement;
}

describe("Форма входа: префилл логина (§85.8)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("на стенде подставляет admin", async () => {
    apiGet.mockResolvedValue({ version: "1.0.0", dev_mode: true });
    renderLogin();
    await waitFor(() => expect(loginField().value).toBe("admin"));
  });

  it("в боевом режиме поле пустое", async () => {
    apiGet.mockResolvedValue({ version: "1.0.0", dev_mode: false });
    renderLogin();
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(loginField().value).toBe("");
  });

  // Старая сборка Web поля не отдаёт вовсе — трактуем как боевое (fail-closed).
  it("без поля dev_mode поле пустое", async () => {
    apiGet.mockResolvedValue({ version: "1.0.0" });
    renderLogin();
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(loginField().value).toBe("");
  });

  it("при недоступном /api/version поле пустое", async () => {
    apiGet.mockRejectedValue(new Error("network"));
    renderLogin();
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(loginField().value).toBe("");
  });

  // Ответ /api/version асинхронный: пришедший позже dev_mode не имеет права
  // затирать то, что оператор уже набрал.
  it("не затирает уже набранный логин", async () => {
    let resolveVersion: (v: unknown) => void = () => {};
    apiGet.mockReturnValue(new Promise((res) => { resolveVersion = res; }));
    renderLogin();

    fireEvent.change(loginField(), { target: { value: "operator" } });
    resolveVersion({ version: "1.0.0", dev_mode: true });

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(loginField().value).toBe("operator");
  });
});
