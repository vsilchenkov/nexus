import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { UsersPanel } from "./Users";
import { ConfirmContext } from "../../lib/confirm";

// §87: выбор роли в диалоге пользователя. Проверяется то, чего не видят ни типы,
// ни бэкенд-тесты: что карточек стало четыре, что выбранная роль реально уходит
// в POST /api/users, и что защита «последний админ» не сломалась о новую роль.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string) => k,
    i18n: { language: "ru" },
  }),
}));

type TestUser = {
  id: string;
  login: string;
  name: string;
  email: string;
  role: string;
  active: boolean;
  lang: "ru";
  must_change_password: boolean;
  default_team_id: string;
  teams: [];
  created_at: string;
};

function user(id: string, login: string, role: string, active = true): TestUser {
  return {
    id,
    login,
    name: login,
    email: "",
    role,
    active,
    lang: "ru",
    must_change_password: false,
    default_team_id: "t1",
    teams: [],
    created_at: "2026-08-01T00:00:00Z",
  };
}

function mockServer(items: TestUser[]) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/auth/me") {
      return Promise.resolve({ user: { user_id: "u-admin", role: "admin" } });
    }
    if (url === "/api/users") return Promise.resolve({ items });
    if (url === "/api/me/teams") return Promise.resolve({ items: [] });
    return Promise.resolve({ items: [] });
  });
  apiPost.mockResolvedValue({});
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <ConfirmContext.Provider value={() => Promise.resolve(true)}>
          {children}
        </ConfirmContext.Provider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return render(<UsersPanel />, { wrapper });
}

// Открывает диалог создания пользователя.
async function openCreateDialog() {
  fireEvent.click(await screen.findByRole("button", { name: /settings\.users\.add/ }));
  await screen.findByText("settings.users.dialog.new_title");
}

const ROLE_CARDS = ["admin", "manager", "operator", "viewer"];

describe("выбор роли в диалоге пользователя", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPost.mockReset();
  });

  it("показывает все четыре роли, включая «Оператор»", async () => {
    mockServer([user("u-admin", "admin", "admin")]);
    renderPanel();
    await openCreateDialog();

    for (const r of ROLE_CARDS) {
      expect(
        screen.getByRole("button", { name: new RegExp(`settings\\.users\\.role\\.${r}\\b`) }),
      ).toBeInTheDocument();
    }
  });

  it("выбранная роль «Оператор» уходит в POST /api/users", async () => {
    mockServer([user("u-admin", "admin", "admin")]);
    renderPanel();
    await openCreateDialog();

    fireEvent.change(screen.getByPlaceholderText("login"), {
      target: { value: "dispatcher" },
    });
    fireEvent.change(
      screen.getByPlaceholderText("settings.users.field.name_placeholder"),
      { target: { value: "Дежурный" } },
    );
    fireEvent.change(screen.getByPlaceholderText("min 8 chars"), {
      target: { value: "Str0ngPassw0rd" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: /settings\.users\.role\.operator\b/ }),
    );
    fireEvent.click(screen.getByRole("button", { name: /settings\.users\.dialog\.create/ }));

    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    // Проверяем именно адрес и полезную нагрузку: клик по карточке может
    // «работать», не доводя роль до запроса (урок §79.11).
    expect(apiPost.mock.calls[0][0]).toBe("/api/users");
    expect(apiPost.mock.calls[0][1]).toMatchObject({ login: "dispatcher", role: "operator" });
  });

  it("последнего активного админа нельзя понизить и в «Оператора»", async () => {
    mockServer([user("u-admin", "admin", "admin")]);
    renderPanel();

    fireEvent.click(await screen.findByTitle("settings.users.action.edit"));
    await screen.findByText(/settings\.users\.dialog\.edit_title/);

    // Все не-админские карточки заблокированы — новая роль не стала лазейкой.
    for (const r of ["manager", "operator", "viewer"]) {
      expect(
        screen.getByRole("button", { name: new RegExp(`settings\\.users\\.role\\.${r}\\b`) }),
      ).toBeDisabled();
    }
    expect(
      screen.getByRole("button", { name: /settings\.users\.role\.admin\b/ }),
    ).not.toBeDisabled();
  });
});
