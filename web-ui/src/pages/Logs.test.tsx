import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import LogsSection from "./Logs";

// §94.7: раздел «Логи» с двумя вкладками. Проверяем две вещи, которые не видит
// ни бэкенд, ни типизация: вкладки — ССЫЛКИ (урок §79: Ctrl+клик и «Открыть в
// новой вкладке» обязаны работать), и «Сервисы» показываются только
// администратору (эндпоинт /api/logs admin-only).

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function renderSection(role: string) {
  apiGet.mockImplementation((url: string) => {
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role } });
    return Promise.resolve({});
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={["/logs/rejected"]}>
      <QueryClientProvider client={qc}>
        <LogsSection />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("LogsSection", () => {
  beforeEach(() => {
    apiGet.mockReset();
  });

  it("администратор видит обе вкладки, и это ссылки с адресами", async () => {
    renderSection("admin");

    const services = await screen.findByText("logs.tabs.services");
    const rejected = screen.getByText("logs.tabs.rejected");

    // Именно <a href>: кнопка выглядела бы так же, но не открывалась бы в новой
    // вкладке ни Ctrl+кликом, ни средней кнопкой мыши.
    expect(services.tagName).toBe("A");
    expect(rejected.tagName).toBe("A");
    expect(services).toHaveAttribute("href", "/logs/services");
    expect(rejected).toHaveAttribute("href", "/logs/rejected");
  });

  it("оператору вкладка служебных логов не показывается", async () => {
    renderSection("operator");

    await waitFor(() => expect(screen.getByText("logs.tabs.rejected")).toBeInTheDocument());
    expect(screen.queryByText("logs.tabs.services")).not.toBeInTheDocument();
  });
});
