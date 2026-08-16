import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import ResetPassword from "./ResetPassword";

// §88.8.3: четыре состояния страницы задания нового пароля. Проверяется то,
// чего не видят ни типы, ни бэкенд-тесты: что недействительная ссылка не
// показывает форму, что кнопка заблокирована до совпадения паролей, и что
// токен из адреса реально доезжает до запроса.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

function renderAt(url: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <Routes>
          <Route path="/reset-password" element={children} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
  return render(<ResetPassword />, { wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
  apiPost.mockResolvedValue(undefined);
});

describe("ResetPassword", () => {
  it("валидная ссылка показывает форму", async () => {
    apiGet.mockResolvedValue({ valid: true });
    renderAt("/reset-password?token=abc");

    expect(await screen.findByText("auth.reset_submit")).toBeInTheDocument();
    expect(apiGet).toHaveBeenCalledWith(
      "/api/auth/password-reset/validate?token=abc",
    );
  });

  it("недействительная ссылка не показывает форму", async () => {
    apiGet.mockResolvedValue({ valid: false });
    renderAt("/reset-password?token=abc");

    expect(await screen.findByText("auth.reset_invalid_hint")).toBeInTheDocument();
    expect(screen.queryByText("auth.reset_submit")).not.toBeInTheDocument();
  });

  // Ошибка проверки — та же картина: различать «нет такой», «истекла» и
  // «использована» наружу нельзя (§88.4.6).
  it("ошибка проверки показывает тот же экран", async () => {
    apiGet.mockRejectedValue(new Error("boom"));
    renderAt("/reset-password?token=abc");

    expect(await screen.findByText("auth.reset_invalid_hint")).toBeInTheDocument();
  });

  // Без токена в адресе в сеть ходить незачем.
  it("адрес без токена не дёргает сервер", async () => {
    renderAt("/reset-password");

    expect(await screen.findByText("auth.reset_invalid_hint")).toBeInTheDocument();
    expect(apiGet).not.toHaveBeenCalled();
  });

  it("кнопка заблокирована, пока пароли не совпали", async () => {
    apiGet.mockResolvedValue({ valid: true });
    renderAt("/reset-password?token=abc");
    await screen.findByText("auth.reset_submit");

    const submit = screen.getByText("auth.reset_submit");
    expect(submit).toBeDisabled();

    const [next, confirm] = screen.getAllByDisplayValue("");
    fireEvent.change(next, { target: { value: "new-strong-password" } });
    expect(submit).toBeDisabled();

    fireEvent.change(confirm, { target: { value: "other-password" } });
    expect(await screen.findByText("auth.reset_mismatch")).toBeInTheDocument();
    expect(submit).toBeDisabled();

    fireEvent.change(confirm, { target: { value: "new-strong-password" } });
    await waitFor(() => expect(submit).toBeEnabled());
  });

  it("короткий пароль не разблокирует кнопку", async () => {
    apiGet.mockResolvedValue({ valid: true });
    renderAt("/reset-password?token=abc");
    await screen.findByText("auth.reset_submit");

    const [next, confirm] = screen.getAllByDisplayValue("");
    fireEvent.change(next, { target: { value: "short" } });
    fireEvent.change(confirm, { target: { value: "short" } });

    expect(screen.getByText("auth.reset_submit")).toBeDisabled();
  });

  it("отправляет токен из адреса и показывает успех", async () => {
    apiGet.mockResolvedValue({ valid: true });
    renderAt("/reset-password?token=tok-42");
    await screen.findByText("auth.reset_submit");

    const [next, confirm] = screen.getAllByDisplayValue("");
    fireEvent.change(next, { target: { value: "new-strong-password" } });
    fireEvent.change(confirm, { target: { value: "new-strong-password" } });
    fireEvent.click(screen.getByText("auth.reset_submit"));

    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    expect(apiPost).toHaveBeenCalledWith("/api/auth/password-reset/confirm", {
      token: "tok-42",
      new_password: "new-strong-password",
    });
    expect(await screen.findByText("auth.reset_done")).toBeInTheDocument();
  });

  // Ссылка могла истечь между проверкой и отправкой — состояние «истекла»
  // обязано показываться и по ошибке подтверждения.
  it("показывает ошибку сервера при подтверждении", async () => {
    apiGet.mockResolvedValue({ valid: true });
    apiPost.mockRejectedValue({ response: { data: { error: "ссылка недействительна" } } });
    renderAt("/reset-password?token=abc");
    await screen.findByText("auth.reset_submit");

    const [next, confirm] = screen.getAllByDisplayValue("");
    fireEvent.change(next, { target: { value: "new-strong-password" } });
    fireEvent.change(confirm, { target: { value: "new-strong-password" } });
    fireEvent.click(screen.getByText("auth.reset_submit"));

    expect(await screen.findByText("ссылка недействительна")).toBeInTheDocument();
    expect(screen.queryByText("auth.reset_done")).not.toBeInTheDocument();
  });
});
