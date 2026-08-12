import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ReplayDialog } from "./ReplayDialog";

// §85.9: идентификатор новой записи приходит из ответа ШИНЫ и потому
// необязателен — у sync-повтора отвечает приёмник, и идентификатора записи в
// его ответе нет. Раньше сервер присылал выдуманный UUID, и строка под
// результатом вела в никуда.

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const BUS_ID = "3f2a1c9e-11aa-4bb2-8cc3-99dd77ee5566";

function renderDialog() {
  apiGet.mockResolvedValue({ id: "log1", parameters: "", request: `{"a":1}` });
  render(
    wrap(<ReplayDialog logId="log1" nodeId="n1" incomingMethod="POST" onClose={() => {}} />),
  );
}

describe("Диалог повтора: идентификатор новой записи (§85.9)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("показывает идентификатор, когда шина его вернула", async () => {
    apiPost.mockResolvedValue({ new_log_id: BUS_ID, status_code: 200 });
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /replay\.send/ }));
    expect(await screen.findByText(BUS_ID)).toBeInTheDocument();
  });

  it("не рисует пустую строку, когда шина идентификатор не вернула", async () => {
    // Ответ sync-повтора: поля new_log_id нет вовсе (omitempty).
    apiGet.mockResolvedValue({ id: "log1", parameters: "", request: `{"a":1}` });
    apiPost.mockResolvedValue({ status_code: 200, body_preview: '{"ok":true}' });
    const { container } = render(
      wrap(<ReplayDialog logId="log1" nodeId="n1" incomingMethod="POST" onClose={() => {}} />),
    );

    fireEvent.click(screen.getByRole("button", { name: /replay\.send/ }));
    await waitFor(() => expect(apiPost).toHaveBeenCalled());
    await screen.findByText("replay.result_status 200");

    expect(container.querySelectorAll("div.font-mono.text-xs")).toHaveLength(0);
  });
});
