import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { GlobalSearch } from "./GlobalSearch";

// §103.4: узел, найденный в шапке ТОЛЬКО по комментарию, показывает фрагмент
// комментария с подсвеченным совпадением; узел, совпавший по пути, — нет.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost, del: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const ITEMS = [
  {
    id: "n-comment",
    path: "svc/orders",
    target_url: "https://api.example.com/orders",
    root_method: "request",
    status: "enabled",
    team_id: "t1",
    team_slug: "logistics",
    team_name: "Логистика",
    comment: "Интеграция с CDEK, тикет NX-41",
  },
  {
    id: "n-path",
    path: "svc/cdek-sync",
    target_url: "https://api.example.com/sync",
    root_method: "request",
    status: "enabled",
    team_id: "t1",
    team_slug: "logistics",
    team_name: "Логистика",
    comment: "Синхронизация CDEK",
  },
];

function renderSearch() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
  return render(<GlobalSearch />, { wrapper });
}

describe("GlobalSearch: сниппет комментария (§103.4)", () => {
  beforeEach(() => {
    sessionStorage.clear();
    apiGet.mockReset();
    apiGet.mockImplementation((url: string) => {
      if (url === "/api/search/nodes") return Promise.resolve({ items: ITEMS });
      if (url === "/api/me/search-history") return Promise.resolve({ items: [] });
      return Promise.resolve({});
    });
  });

  it("сниппет только у узла, найденного по комментарию", async () => {
    renderSearch();
    const input = screen.getByPlaceholderText("search.placeholder");
    fireEvent.focus(input);
    fireEvent.change(input, { target: { value: "cdek" } });

    const mark = await waitFor(
      () => {
        const marks = document.querySelectorAll("mark");
        expect(marks).toHaveLength(1);
        return marks[0];
      },
      { timeout: 2000 },
    );
    expect(mark).toHaveTextContent("CDEK");
    expect(screen.getByRole("img", { name: "search.comment_match" })).toBeInTheDocument();

    // Сниппет — в строке узла, найденного по комментарию, а не у соседа.
    const row = mark.closest("[cmdk-item]") as HTMLElement;
    expect(row).toHaveTextContent("svc/orders");
    expect(row).toHaveTextContent("Интеграция с CDEK, тикет NX-41");

    const pathRow = screen.getByText("svc/cdek-sync").closest("[cmdk-item]") as HTMLElement;
    expect(pathRow.querySelector("mark")).toBeNull();

    expect(apiGet).toHaveBeenCalledWith("/api/search/nodes", { q: "cdek", limit: 20 });
  });
});
