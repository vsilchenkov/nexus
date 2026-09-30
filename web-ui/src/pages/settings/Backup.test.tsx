import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackupPanel } from "./Backup";
import type { BackupStatus, ImportPreview } from "../../components/backup/types";

// §110.9: страница «Резервная копия». Проверяется то, что не видят серверные
// тесты: на нечистом экземпляре выбрать файл нельзя, пароль выгрузки
// вводится дважды, применение уходит только после ввода адреса сервера и
// несёт дайджест ТОГО предпросмотра, что показан (смена опций его обновляет).

const { apiGet, apiPost, apiPostBlob } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPostBlob: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, post: apiPost, postBlob: apiPostBlob } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

const cleanStatus: BackupStatus = {
  nodes: 0,
  teams: 1,
  users: 1,
  api_tokens: 0,
  clean: true,
  clickhouse_available: true,
  instance_id: "nw",
  nexus_version: "v1.40.0",
  schema_version: 43,
  min_passphrase_len: 12,
};

function plan(over: Partial<ImportPreview> = {}): ImportPreview {
  return {
    header: {
      exported_at: "2026-09-29T17:30:00Z",
      nexus_version: "v1.39.0",
      schema_version: 43,
      source_instance_id: "kz",
      counts: { nodes: 2 },
    },
    exported_by: "root",
    source_public_url: "https://nexus-kz.example.ru",
    entities: { teams: { create: 1, existing: 1 }, nodes: { create: 2, existing: 0 } },
    skipped: [],
    databases: [{ team: "acme", name: "nexus_nw_acme", ownership: "none", tables: 0, action: "create" }],
    tables: { create: 2, existing: 0, external: 0, external_missing: 0, unmanaged: 0 },
    node_statuses: { enabled: 2, paused: 0, disabled: 0 },
    settings_sections: ["sentry"],
    warnings: [],
    blockers: [],
    digest: "digest-1",
    ...over,
  };
}

function mockStatus(status: BackupStatus) {
  apiGet.mockImplementation((url: string) =>
    url === "/api/backup/status" ? Promise.resolve(status) : Promise.resolve({}),
  );
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

// pickFile — выбор файла; text() задан явно, чтобы не зависеть от версии jsdom.
function pickFile(content: string) {
  const f = new File([content], "nexus-backup-kz.json", { type: "application/json" });
  Object.defineProperty(f, "text", { value: () => Promise.resolve(content) });
  fireEvent.change(screen.getByLabelText("settings.backup.restore.file"), { target: { files: [f] } });
}

async function checkFile(content = '{"format":"nexus-config-backup"}') {
  render(<BackupPanel />, { wrapper });
  await screen.findByText("settings.backup.restore.clean");
  pickFile(content);
  await screen.findByText("nexus-backup-kz.json");
  const inputs = document.querySelectorAll('input[type="password"]');
  fireEvent.change(inputs[inputs.length - 1], { target: { value: "file-password-1" } });
  fireEvent.click(screen.getByText("settings.backup.restore.check"));
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("BackupPanel", () => {
  it("на нечистом экземпляре выбор файла выключен и объяснён", async () => {
    mockStatus({ ...cleanStatus, clean: false, nodes: 142, teams: 6 });
    render(<BackupPanel />, { wrapper });

    expect(await screen.findByText("settings.backup.restore.not_clean")).toBeInTheDocument();
    expect(screen.getByText("settings.backup.restore.choose_file")).toBeDisabled();
    expect(screen.queryByLabelText("settings.backup.restore.file")).not.toBeInTheDocument();
    // Выгрузка на боевом экземпляре — основной сценарий, она доступна.
    expect(screen.getByText("settings.backup.export.download")).toBeInTheDocument();
  });

  it("без ClickHouse восстановление выключено", async () => {
    mockStatus({ ...cleanStatus, clickhouse_available: false });
    render(<BackupPanel />, { wrapper });
    expect(await screen.findByText("settings.backup.restore.no_clickhouse")).toBeInTheDocument();
  });

  it("выгрузка — только после ввода пароля дважды, пароль уходит телом", async () => {
    mockStatus(cleanStatus);
    apiPostBlob.mockResolvedValue({ blob: new Blob(["{}"]), filename: "nexus-backup-nw.json" });
    const createURL = vi.fn(() => "blob:backup");
    Object.assign(URL, { createObjectURL: createURL, revokeObjectURL: vi.fn() });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    render(<BackupPanel />, { wrapper });
    await screen.findByText("settings.backup.restore.clean");

    const download = screen.getByText("settings.backup.export.download").closest("button")!;
    const [pass, repeat] = document.querySelectorAll('input[type="password"]');
    fireEvent.change(pass, { target: { value: "short" } });
    fireEvent.change(repeat, { target: { value: "short" } });
    expect(download).toBeDisabled();

    fireEvent.change(pass, { target: { value: "long-enough-pass" } });
    fireEvent.change(repeat, { target: { value: "long-enough-pas" } });
    expect(screen.getByText("settings.backup.export.mismatch")).toBeInTheDocument();
    expect(download).toBeDisabled();

    fireEvent.change(repeat, { target: { value: "long-enough-pass" } });
    fireEvent.click(download);

    await waitFor(() => expect(apiPostBlob).toHaveBeenCalledWith("/api/backup/export", { passphrase: "long-enough-pass" }));
    expect(await screen.findByText("nexus-backup-nw.json")).toBeInTheDocument();
    expect(click).toHaveBeenCalled();
    expect(createURL).toHaveBeenCalled();
  });

  it("проверка → подтверждение адресом → применение с дайджестом предпросмотра", async () => {
    mockStatus(cleanStatus);
    apiPost.mockImplementation((url: string) =>
      url === "/api/backup/import/preview"
        ? Promise.resolve(plan())
        : Promise.resolve({ preview: plan(), databases_claimed: 1, tables_created: 2, tables_updated: 0, duration_ms: 1200 }),
    );
    await checkFile('{"format":"nexus-config-backup","n":1}');

    await screen.findByText("settings.backup.restore.dry_run_note");
    const [, body, opts] = apiPost.mock.calls[0];
    expect(body).toEqual({
      file: '{"format":"nexus-config-backup","n":1}',
      passphrase: "file-password-1",
      options: { status_mode: "pause_pull", adopt_unclaimed: false },
    });
    expect(opts.timeout).toBeGreaterThan(30_000);

    fireEvent.click(screen.getByText("settings.backup.restore.apply"));
    const submit = screen.getByText("settings.backup.confirm.submit").closest("button")!;
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/settings.backup.confirm.type_host/), {
      target: { value: "nexus-prod.example.ru" },
    });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/settings.backup.confirm.type_host/), {
      target: { value: ` ${window.location.hostname.toUpperCase()} ` },
    });
    fireEvent.click(submit);

    await screen.findByText("settings.backup.result.done");
    const [url, applyBody, applyOpts] = apiPost.mock.calls[1];
    expect(url).toBe("/api/backup/import/apply");
    expect(applyBody.preview_digest).toBe("digest-1");
    expect(applyBody.confirm_host).toBe(window.location.hostname.toUpperCase());
    expect(applyBody.file).toBe('{"format":"nexus-config-backup","n":1}');
    expect(applyOpts.timeout).toBeGreaterThanOrEqual(10 * 60_000);
  });

  it("блокирующая проблема выключает «Восстановить»", async () => {
    mockStatus(cleanStatus);
    apiPost.mockResolvedValue(
      plan({ blockers: [{ code: "ch_database_foreign", subject: "nexus_nw_acme", detail: "msk" }] }),
    );
    await checkFile();

    expect(await screen.findByText("settings.backup.issue.ch_database_foreign")).toBeInTheDocument();
    expect(screen.getByText("settings.backup.restore.apply")).toBeDisabled();
  });

  it("смена режима статусов перезапускает предпросмотр, применяется новый дайджест", async () => {
    mockStatus(cleanStatus);
    apiPost
      .mockResolvedValueOnce(plan({ digest: "digest-pull" }))
      .mockResolvedValueOnce(plan({ digest: "digest-all" }))
      .mockResolvedValueOnce({ preview: plan(), databases_claimed: 0, tables_created: 0, tables_updated: 0, duration_ms: 10 });
    await checkFile();
    await screen.findByText("settings.backup.restore.dry_run_note");

    fireEvent.click(screen.getByLabelText("settings.backup.status_mode.pause_all"));
    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(2));
    expect(apiPost.mock.calls[1][1].options.status_mode).toBe("pause_all");

    await waitFor(() => expect(screen.getByText("settings.backup.restore.apply")).not.toBeDisabled());
    fireEvent.click(screen.getByText("settings.backup.restore.apply"));
    fireEvent.change(screen.getByLabelText(/settings.backup.confirm.type_host/), {
      target: { value: window.location.hostname },
    });
    fireEvent.click(screen.getByText("settings.backup.confirm.submit"));

    await waitFor(() => expect(apiPost).toHaveBeenCalledTimes(3));
    expect(apiPost.mock.calls[2][1].preview_digest).toBe("digest-all");
    expect(apiPost.mock.calls[2][1].options.status_mode).toBe("pause_all");
  });

  it("галочка «занять базы» — только когда есть непустая база без маркера", async () => {
    mockStatus(cleanStatus);
    apiPost.mockResolvedValue(
      plan({
        databases: [{ team: "acme", name: "nexus_nw_acme", ownership: "unclaimed", tables: 4, action: "blocked" }],
        blockers: [{ code: "ch_database_unclaimed", subject: "nexus_nw_acme", detail: "4" }],
      }),
    );
    await checkFile();

    expect(await screen.findByText("settings.backup.preview.adopt")).toBeInTheDocument();
  });

  it("ошибка сервера переводится по коду в языке интерфейса", async () => {
    mockStatus(cleanStatus);
    apiPost.mockRejectedValue({
      response: { data: { error: "wrong password", code: "backup.decrypt_failed" } },
    });
    await checkFile();
    expect(await screen.findByText("backup.decrypt_failed")).toBeInTheDocument();
  });
});
