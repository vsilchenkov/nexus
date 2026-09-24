import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RevertAuditDialog } from "./RevertAuditDialog";
import type { RevertPreview } from "./revert";

// §108.8: диалог отмены. Выбор по умолчанию — поля, где сейчас «Стало», без
// предупреждения о кредах; updated_at узла уходит в POST строкой как пришла
// (Date съел бы микросекунды, и сервер отвечал бы 409 всегда).

const { apiGet, apiPost } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPost: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const UPDATED_AT = "2026-09-24T10:00:00.123456Z";

function preview(over: Partial<RevertPreview> = {}): RevertPreview {
  return {
    entry: { id: "e1", action: "node.update", created_at: "2026-09-23T18:36:22Z", user_login: "alice" },
    node: { id: "n1", path: "svc/x", team_id: "t1", updated_at: UPDATED_AT },
    state: "stale",
    moved: false,
    fields: [
      { name: "status", before: "paused", after: "enabled", current: "enabled", status: "actual", warning: "" },
      { name: "target_url", before: "http://a", after: "http://b", current: "http://c", status: "overwritten", warning: "" },
      { name: "auth_type", before: "basic", after: "token", current: "token", status: "actual", warning: "credentials" },
      { name: "timeout_ms", before: 5000, after: 7000, current: 5000, status: "already", warning: "" },
      { name: "auth_credentials", before: "changed", after: "changed", current: null, status: "secret", warning: "" },
    ],
    dry_run: { code: "", field: "", not_held: [] },
    ...over,
  };
}

function renderDialog(onClose = vi.fn()) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <RevertAuditDialog entryId="e1" onClose={onClose} />
    </QueryClientProvider>,
  );
  return onClose;
}

function checkbox(name: string) {
  return screen.queryByLabelText<HTMLInputElement>(name);
}

describe("RevertAuditDialog (§108.8)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiGet.mockResolvedValue(preview());
    apiPost.mockResolvedValue(undefined);
  });

  it("по умолчанию отмечены только актуальные поля без предупреждения", async () => {
    renderDialog();
    await waitFor(() => expect(checkbox("status")).not.toBeNull());

    expect(checkbox("status")!.checked).toBe(true);
    expect(checkbox("target_url")!.checked).toBe(false);
    expect(checkbox("auth_type")!.checked).toBe(false);
    expect(checkbox("timeout_ms")).toBeNull();
    expect(checkbox("auth_credentials")).toBeNull();
    expect(screen.getByText("audit.revert.overwritten")).toBeInTheDocument();
    expect(screen.getByText("audit.revert.credentials")).toBeInTheDocument();
    expect(screen.getByText("audit.revert.status.already")).toBeInTheDocument();
    expect(screen.getByText("audit.revert.status.secret")).toBeInTheDocument();
  });

  it("применение шлёт выбранные поля и updated_at строкой как пришла", async () => {
    const onClose = renderDialog();
    await waitFor(() => expect(checkbox("target_url")).not.toBeNull());
    fireEvent.click(checkbox("target_url")!);
    fireEvent.click(screen.getByText("audit.revert.submit"));

    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(1));
    const [url, body] = apiPost.mock.calls[0];
    expect(url).toBe("/api/audit/revert/e1");
    expect(body).toEqual({ fields: ["status", "target_url"], expected_updated_at: UPDATED_AT });
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("409 конфликт — сообщение и перечитанный предпросмотр", async () => {
    apiPost.mockRejectedValue({ response: { status: 409, data: { code: "audit.revert.conflict" } } });
    const onClose = renderDialog();
    await waitFor(() => expect(checkbox("status")).not.toBeNull());
    fireEvent.click(screen.getByText("audit.revert.submit"));

    expect(await screen.findByText("audit.revert.conflict_refreshed")).toBeInTheDocument();
    await waitFor(() => expect(apiGet).toHaveBeenCalledTimes(2));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("пока предпросмотр перечитывается после 409, применить нельзя", async () => {
    apiPost.mockRejectedValue({ response: { status: 409, data: { code: "audit.revert.conflict" } } });
    renderDialog();
    await waitFor(() => expect(checkbox("status")).not.toBeNull());
    let release: (v: RevertPreview) => void = () => {};
    apiGet.mockImplementationOnce(() => new Promise<RevertPreview>((r) => (release = r)));
    fireEvent.click(screen.getByText("audit.revert.submit"));

    await screen.findByText("audit.revert.conflict_refreshed");
    const submit = screen.getByText("audit.revert.submit").closest("button");
    await waitFor(() => expect(submit).toBeDisabled());
    release(preview({ node: { id: "n1", path: "svc/x", team_id: "t1", updated_at: "2026-09-24T11:00:00Z" } }));
    await waitFor(() => expect(submit).not.toBeDisabled());
  });

  it("значения не удержались — код и поля в сообщении", async () => {
    apiPost.mockRejectedValue({
      response: { status: 400, data: { code: "audit.revert.not_held", error: "x", fields: ["sse_decline"] } },
    });
    renderDialog();
    await waitFor(() => expect(checkbox("status")).not.toBeNull());
    fireEvent.click(screen.getByText("audit.revert.submit"));

    expect(await screen.findByText("audit.revert.not_held: sse_decline")).toBeInTheDocument();
  });

  it("узел удалён — применить нечего", async () => {
    apiGet.mockResolvedValue(preview({ node: null, state: "gone" }));
    renderDialog();

    expect(await screen.findByText("audit.revert.gone")).toBeInTheDocument();
    expect(checkbox("status")).toBeNull();
    expect(screen.getByText("audit.revert.submit").closest("button")).toBeDisabled();
  });

  it("пробный прогон выбора по умолчанию предупреждает о сбросе", async () => {
    apiGet.mockResolvedValue(
      preview({ dry_run: { code: "audit.revert.not_held", field: "", not_held: ["sse_decline"] } }),
    );
    renderDialog();
    expect(await screen.findByText("audit.revert.dry_run")).toBeInTheDocument();
  });
});
