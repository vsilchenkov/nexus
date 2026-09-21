import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { HostAllowlistEntry } from "../../api/client";
import { AllowedHostsField } from "./AllowedHostsField";

// §104.6: привязка и отвязка хоста в форме узла меняют usage_count — счётчик
// «узлов: N» в Настройках и открытые списки узлов обязаны перечитаться сразу,
// а не через 30 с staleTime.

const { apiGet, apiPost, apiDel } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiDel: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { get: apiGet, post: apiPost, del: apiDel } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const host = (id: string, pattern: string): HostAllowlistEntry => ({
  id,
  pattern,
  kind: "exact",
  description: "",
  usage_count: 1,
  created_by: "admin",
  created_at: "",
  updated_at: "",
});

function renderField() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(qc, "invalidateQueries");
  render(
    <QueryClientProvider client={qc}>
      <AllowedHostsField nodeId="n1" urlMode="from_request" pending={[]} onPendingChange={() => {}} />
    </QueryClientProvider>,
  );
  const keys = () => invalidate.mock.calls.map((c) => (c[0] as { queryKey: unknown[] }).queryKey[0]);
  return { keys };
}

describe("AllowedHostsField: счётчики справочника (§104.6)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiGet.mockImplementation((url: string) =>
      Promise.resolve({
        items: url === "/api/nodes/n1/allowed-hosts" ? [host("h1", "api.partner.com")] : [host("h2", "cdn.example.com")],
      }),
    );
    apiPost.mockReset();
    apiPost.mockResolvedValue({});
    apiDel.mockReset();
    apiDel.mockResolvedValue({});
  });

  it("отвязка сбрасывает список хостов узла, справочник и списки узлов", async () => {
    const { keys } = renderField();
    fireEvent.click(await screen.findByLabelText("common.delete"));

    await waitFor(() => expect(apiDel).toHaveBeenCalledWith("/api/nodes/n1/allowed-hosts/h1"));
    await waitFor(() =>
      expect(keys()).toEqual(expect.arrayContaining(["node-hosts", "allowed-hosts", "catalog-nodes"])),
    );
  });

  it("привязка сбрасывает то же", async () => {
    const { keys } = renderField();
    await screen.findByText("api.partner.com");
    fireEvent.click(screen.getByText("node.allowed_hosts.add_placeholder"));
    fireEvent.click(await screen.findByText("cdn.example.com"));

    await waitFor(() =>
      expect(apiPost).toHaveBeenCalledWith("/api/nodes/n1/allowed-hosts", { host_id: "h2" }),
    );
    await waitFor(() =>
      expect(keys()).toEqual(expect.arrayContaining(["node-hosts", "allowed-hosts", "catalog-nodes"])),
    );
  });
});
