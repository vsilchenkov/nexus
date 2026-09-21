import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { type ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AllowedHostsPanel } from "./AllowedHosts";
import { HeadersPanel } from "./Headers";

// §104.5 на страницах «Заголовки» и «Разрешённые хосты»: счётчик «узлов: N» —
// кнопка, открывающая список узлов; «не используется» — текст. Группы
// проверяет NodeGroups.test.tsx.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { get: apiGet, post: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

vi.mock("../../lib/confirm", () => ({ useConfirm: () => () => Promise.resolve(true) }));

const entry = { description: "", created_by: "admin", created_at: "", updated_at: "" };

const cases = [
  {
    name: "Заголовки",
    panel: <HeadersPanel />,
    ns: "settings.headers",
    title: "catalog_nodes.title.header",
    nodesUrl: "/api/headers/h1/nodes",
    items: [
      { ...entry, id: "h1", name: "X-Request-ID", usage_count: 3 },
      { ...entry, id: "h2", name: "X-Unused", usage_count: 0 },
    ],
    usedText: "X-Request-ID",
    freeText: "X-Unused",
  },
  {
    name: "Разрешённые хосты",
    panel: <AllowedHostsPanel />,
    ns: "settings.allowed_hosts",
    title: "catalog_nodes.title.host",
    nodesUrl: "/api/allowed-hosts/h1/nodes",
    items: [
      { ...entry, id: "h1", pattern: "api.partner.com", kind: "exact", usage_count: 2 },
      { ...entry, id: "h2", pattern: "*.idle.io", kind: "wildcard", usage_count: 0 },
    ],
    usedText: "api.partner.com",
    freeText: "*.idle.io",
  },
];

function renderPanel(panel: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>{panel}</QueryClientProvider>
    </MemoryRouter>,
  );
}

const rowOf = (text: string) => screen.getByText(text).closest("tr") as HTMLElement;

describe.each(cases)("$name: узлы элемента (§104.5)", (c) => {
  beforeEach(() => {
    apiGet.mockReset();
    apiGet.mockImplementation((url: string) =>
      Promise.resolve(
        url === c.nodesUrl ? { items: [], total: 2, visible: 0, hidden: 2 } : { items: c.items },
      ),
    );
  });

  it("счётчик — кнопка, открывающая список узлов", async () => {
    renderPanel(c.panel);
    await waitFor(() => expect(screen.getByText(c.usedText)).toBeInTheDocument());

    const counter = within(rowOf(c.usedText)).getByRole("button", { name: `${c.ns}.used` });
    fireEvent.click(counter);
    expect(await screen.findByText(c.title)).toBeInTheDocument();
    expect(apiGet).toHaveBeenCalledWith(c.nodesUrl);
  });

  it("«не используется» — не кнопка", async () => {
    renderPanel(c.panel);
    await waitFor(() => expect(screen.getByText(c.freeText)).toBeInTheDocument());
    const row = rowOf(c.freeText);
    expect(within(row).getByText(`${c.ns}.not_used`)).toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: `${c.ns}.not_used` })).toBeNull();
  });

  it("подсказка заблокированного удаления ведёт к списку узлов", async () => {
    renderPanel(c.panel);
    await waitFor(() => expect(screen.getByText(c.usedText)).toBeInTheDocument());
    expect(within(rowOf(c.usedText)).getByTitle(`${c.ns}.delete_in_use_tip`)).toBeDisabled();
  });
});
