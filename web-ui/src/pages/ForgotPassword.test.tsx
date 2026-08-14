import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Login from "./Login";

// §88.8.1–88.8.2: ссылка «Забыли пароль?» и диалог восстановления.
//
// Ключевое здесь — видимость ссылки: она обязана появляться ТОЛЬКО когда
// восстановление действительно работоспособно. Иначе пользователь запрашивает
// письмо, которого физически не будет.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
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

beforeEach(() => {
  vi.clearAllMocks();
  apiPost.mockResolvedValue({ ok: true, ttl_minutes: 60 });
});

describe("ссылка «Забыли пароль?»", () => {
  it("показывается, когда восстановление доступно", async () => {
    apiGet.mockResolvedValue({ dev_mode: false, password_reset_ready: true });
    renderLogin();

    expect(await screen.findByText("auth.forgot_link")).toBeInTheDocument();
  });

  it("скрыта, когда восстановление недоступно", async () => {
    apiGet.mockResolvedValue({ dev_mode: false, password_reset_ready: false });
    renderLogin();

    await screen.findByText("auth.login_button");
    expect(screen.queryByText("auth.forgot_link")).not.toBeInTheDocument();
  });

  // Fail-closed: недоступный /api/version не должен показывать ссылку.
  it("скрыта при недоступном /api/version", async () => {
    apiGet.mockRejectedValue(new Error("offline"));
    renderLogin();

    await screen.findByText("auth.login_button");
    expect(screen.queryByText("auth.forgot_link")).not.toBeInTheDocument();
  });

  // Элемент открывает диалог, а не ведёт по адресу — значит это кнопка, и она
  // не должна сабмитить форму входа (§88.8.1).
  it("это кнопка type=button, а не ссылка", async () => {
    apiGet.mockResolvedValue({ password_reset_ready: true });
    renderLogin();

    const el = await screen.findByText("auth.forgot_link");
    expect(el.tagName).toBe("BUTTON");
    expect(el).toHaveAttribute("type", "button");
  });
});

describe("диалог восстановления", () => {
  async function openDialog() {
    apiGet.mockResolvedValue({ password_reset_ready: true });
    renderLogin();
    fireEvent.click(await screen.findByText("auth.forgot_link"));
    return screen.findByText("auth.forgot_title");
  }

  it("переносит набранный логин в поле диалога", async () => {
    apiGet.mockResolvedValue({ password_reset_ready: true });
    renderLogin();

    const loginField = screen.getByRole("textbox") as HTMLInputElement;
    fireEvent.change(loginField, { target: { value: "ivanov" } });
    fireEvent.click(await screen.findByText("auth.forgot_link"));

    await screen.findByText("auth.forgot_title");
    // Значение теперь в двух полях — на форме входа и в диалоге; проверяем
    // именно диалоговое (последний textbox в документе).
    const dialogField = screen.getAllByRole("textbox").at(-1) as HTMLInputElement;
    expect(dialogField.value).toBe("ivanov");
  });

  it("отправляет запрос и показывает единый ответ", async () => {
    await openDialog();

    const field = screen.getAllByRole("textbox").at(-1) as HTMLInputElement;
    fireEvent.change(field, { target: { value: "ivanov" } });
    fireEvent.click(screen.getByText("auth.forgot_submit"));

    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    expect(apiPost).toHaveBeenCalledWith("/api/auth/password-reset/request", {
      login: "ivanov",
    });
    expect(await screen.findByText("auth.forgot_sent")).toBeInTheDocument();
    // Форма ввода уступает место сообщению: повторная отправка не нужна.
    expect(screen.queryByText("auth.forgot_submit")).not.toBeInTheDocument();
  });

  it("блокирует отправку при пустом вводе", async () => {
    await openDialog();
    expect(screen.getByText("auth.forgot_submit")).toBeDisabled();
  });

  // 429 обязан оставлять форму: пользователь повторит через минуту.
  it("показывает ошибку лимита и не закрывает форму", async () => {
    apiPost.mockRejectedValue({ response: { data: { error: "слишком много запросов" } } });
    await openDialog();

    const field = screen.getAllByRole("textbox").at(-1) as HTMLInputElement;
    fireEvent.change(field, { target: { value: "ivanov" } });
    fireEvent.click(screen.getByText("auth.forgot_submit"));

    expect(await screen.findByText("слишком много запросов")).toBeInTheDocument();
    expect(screen.getByText("auth.forgot_submit")).toBeInTheDocument();
    expect(screen.queryByText("auth.forgot_sent")).not.toBeInTheDocument();
  });
});
