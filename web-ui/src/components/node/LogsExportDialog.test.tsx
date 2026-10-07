import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { LogsExportDialog } from "./LogsExportDialog";
import { LogsTab } from "./LogsTab";
import { type Node } from "../../api/client";
import { emptyAdvForm, type LogsFilterState } from "../../lib/logsQuery";
import { type LogsExportEstimate } from "../../lib/logsExport";

// §115.4: диалог выгрузки журнала в Excel — стартовые фильтры с вкладки,
// живая оценка, параметры выгрузки, отказы сервера, «Остановить».

const { apiGet, apiGetBlob, saveBlob } = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiGetBlob: vi.fn(),
  saveBlob: vi.fn(),
}));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet, getBlob: apiGetBlob } };
});

vi.mock("../../lib/download", () => ({ saveBlob }));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
    i18n: { language: "ru" },
  }),
}));

const NODE = {
  id: "n1",
  path: "gate/orders",
  root_method: "request",
  clickhouse_table: "nexus_default.orders",
} as unknown as Node;

const ESTIMATE_URL = "/api/nodes/n1/logs/export/estimate";
const EXPORT_URL = "/api/nodes/n1/logs/export.xlsx";

const EST: LogsExportEstimate = {
  records: 1234,
  rows: 1300,
  request_bytes: 1300 * 1000,
  response_bytes: 0,
  max_rows: 50_000,
  max_bytes: 100 << 20,
  max_body_chars: 32_767,
  logs_configured: true,
  logs_available: true,
};

function mockServer(est: Partial<LogsExportEstimate> = {}, role = "viewer") {
  apiGet.mockImplementation((url: string) => {
    if (url === ESTIMATE_URL) return Promise.resolve({ ...EST, ...est });
    if (url === "/api/auth/me") return Promise.resolve({ user: { user_id: "u1", role } });
    if (url === "/api/nodes/n1/logs") return Promise.resolve({ items: [], logs_available: true });
    if (url === "/api/nodes/n1/logs/count") return Promise.resolve({ total: 0, logs_available: true });
    return Promise.resolve({ items: [] });
  });
}

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <MemoryRouter>
      <QueryClientProvider client={qc}>
        <TooltipProvider>{children}</TooltipProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

const BASE: LogsFilterState = { ...emptyAdvForm, status: "all", done: "all" };

function renderDialog(filter: Partial<LogsFilterState> = {}, onClose = vi.fn()) {
  return render(wrap(<LogsExportDialog node={NODE} filter={{ ...BASE, ...filter }} onClose={onClose} />));
}

// estimateParams — параметры последнего запроса оценки.
function estimateParams(): Record<string, string> {
  const calls = apiGet.mock.calls.filter((c) => c[0] === ESTIMATE_URL);
  return calls[calls.length - 1][1] as Record<string, string>;
}

async function waitEstimate() {
  await screen.findByText(/^logs\.export\.found:/);
}

describe("LogsExportDialog (§115.4)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiGetBlob.mockReset();
    saveBlob.mockReset();
  });

  it("показывает фильтры вкладки сводкой без правки и оценивает под ними", async () => {
    mockServer();
    renderDialog({ q: "order_id", status: "err", from: "2026-10-07T00:00" });

    await waitEstimate();
    // Второй панели фильтров в форме нет — только сводка.
    expect(screen.queryByPlaceholderText("logs.advanced.q_placeholder")).toBeNull();
    expect(screen.queryByRole("button", { name: "logs.advanced.reset" })).toBeNull();
    expect(screen.getByText("order_id")).toBeInTheDocument();
    expect(screen.getByText("logs.filter.err")).toBeInTheDocument();
    expect(estimateParams()).toMatchObject({ q: "order_id", status: "err" });
    expect(estimateParams().from).toBeDefined();
    expect(screen.getByText(/^logs\.export\.found:/).textContent).toContain(`"records":"${(1234).toLocaleString()}"`);
  });

  it("выгрузка шлёт фильтр, потолок строк, тела, пояс и язык интерфейса; файл сохраняется", async () => {
    mockServer();
    const blob = new Blob(["xlsx"]);
    apiGetBlob.mockResolvedValue({ blob, filename: "nexus-logs-gate_orders.xlsx" });
    renderDialog({ q: "order_id" });
    await waitEstimate();

    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));

    await screen.findByText(/^logs\.export\.saved:/);
    const [url, params, opts] = apiGetBlob.mock.calls[0];
    expect(url).toBe(EXPORT_URL);
    expect(params).toMatchObject({ q: "order_id", rows: "50000", bodies: "1" });
    expect(params.tz).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    expect(opts.headers).toEqual({ "Accept-Language": "ru" });
    expect(opts.signal).toBeInstanceOf(AbortSignal);
    expect(saveBlob).toHaveBeenCalledWith(blob, "nexus-logs-gate_orders.xlsx");
  });

  it("пресет строк и снятые тела доходят до запроса, предупреждение о телах пропадает", async () => {
    mockServer();
    apiGetBlob.mockResolvedValue({ blob: new Blob([]) });
    renderDialog({ from: "2026-10-07T00:00" });
    await waitEstimate();
    expect(screen.getByText("logs.export.warn_bodies")).toBeInTheDocument();

    const rows = screen.getByLabelText("logs.export.rows_label");
    expect(rows).toHaveValue("max");
    expect(screen.getByRole("option", { name: (50_000).toLocaleString() })).toBeInTheDocument();
    fireEvent.change(rows, { target: { value: "1000" } });
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.queryByText("logs.export.warn_bodies")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));

    await waitFor(() => expect(saveBlob).toHaveBeenCalledWith(expect.any(Blob), "nexus-logs.xlsx"));
    const params = apiGetBlob.mock.calls[0][1];
    expect(params.rows).toBe("1000");
    expect(params.bodies).toBeUndefined();
  });

  it("429 — текст про частоту, файл не сохраняется", async () => {
    mockServer();
    apiGetBlob.mockRejectedValue({ response: { status: 429, data: { error: "rate limited" } } });
    renderDialog();
    await waitEstimate();

    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));

    expect(await screen.findByText("logs.export.err_rate")).toBeInTheDocument();
    expect(saveBlob).not.toHaveBeenCalled();
  });

  it("«Остановить» обрывает запрос; закрытие диалога посреди выгрузки — тоже", async () => {
    mockServer();
    const signals: AbortSignal[] = [];
    apiGetBlob.mockImplementation(
      (_u: string, _p: unknown, opts: { signal: AbortSignal; onDownloadProgress: (n: number) => void }) =>
        new Promise((_, reject) => {
          signals.push(opts.signal);
          opts.onDownloadProgress(4096);
          opts.signal.addEventListener("abort", () => reject({ code: "ERR_CANCELED" }));
        }),
    );
    const view = renderDialog();
    await waitEstimate();

    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));
    await screen.findByText(/^logs\.export\.running:/);
    fireEvent.click(screen.getByRole("button", { name: "logs.export.stop" }));

    expect(await screen.findByText("logs.export.stopped")).toBeInTheDocument();
    expect(signals[0].aborted).toBe(true);
    expect(saveBlob).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));
    await screen.findByText(/^logs\.export\.running:/);
    view.unmount();
    expect(signals[1].aborted).toBe(true);
  });

  it("done с дип-линка «Очереди» виден в сводке и уходит в оценку и выгрузку", async () => {
    mockServer();
    apiGetBlob.mockResolvedValue({ blob: new Blob([]) });
    renderDialog({ done: "pending" });
    await waitEstimate();

    expect(screen.getByText("logs.export.run")).toBeInTheDocument();
    expect(screen.getByText("logs.filter.done_no")).toBeInTheDocument();
    expect(estimateParams().done).toBe("no");
    fireEvent.click(screen.getByRole("button", { name: "logs.export.submit" }));
    await waitFor(() => expect(apiGetBlob.mock.calls[0][1].done).toBe("no"));
  });

  it("оценка недоступна — выгрузить всё равно можно", async () => {
    mockServer({ logs_available: false });
    renderDialog();

    expect(await screen.findByText("logs.export.estimate_unavailable")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "logs.export.submit" })).toBeEnabled();
  });

  it("под фильтрами пусто — выгружать нечего", async () => {
    mockServer({ records: 0, rows: 0, request_bytes: 0 });
    renderDialog();

    expect(await screen.findByText("logs.export.nothing")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "logs.export.submit" })).toBeDisabled();
  });

  it("без периода — предупреждение; с поиском — про всю историю", async () => {
    mockServer();
    const { unmount } = renderDialog();
    expect(await screen.findByText("logs.export.warn_no_period")).toBeInTheDocument();
    unmount();

    renderDialog({ q: "x" });
    expect(await screen.findByText("logs.export.warn_no_period_search")).toBeInTheDocument();
  });

  it("упор в лимит объёма — предупреждение без обещанного числа записей", async () => {
    mockServer({ max_bytes: 100_000 });
    renderDialog();

    const warn = await screen.findByText(/^logs\.export\.limited_bytes:/);
    expect(warn.textContent).not.toContain('"n"');
  });
});

describe("LogsTab — кнопка «Выгрузить» (§115)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiGetBlob.mockReset();
  });

  it("у наблюдателя кнопка рядом со «Сбросить» открывает диалог с фильтрами вкладки", async () => {
    mockServer({}, "viewer");
    render(wrap(<LogsTab node={NODE} initialFilter={{ q: "order_id", status: "err" }} />));

    fireEvent.click(await screen.findByRole("button", { name: "logs.export.button" }));

    expect(await screen.findByText("logs.export.title")).toBeInTheDocument();
    await waitEstimate();
    expect(estimateParams()).toMatchObject({ q: "order_id", status: "err" });
  });

  it("поиск, набранный без Enter, применяется кнопкой — и в журнале, и в выгрузке", async () => {
    mockServer();
    render(wrap(<LogsTab node={NODE} initialFilter={{ method: "orders.create" }} />));

    fireEvent.change(await screen.findByPlaceholderText("logs.advanced.q_placeholder"), {
      target: { value: "typed" },
    });
    fireEvent.click(screen.getByRole("button", { name: "logs.export.button" }));

    await waitEstimate();
    expect(estimateParams()).toMatchObject({ q: "typed", method: "orders.create" });
    const list = apiGet.mock.calls.filter((c) => c[0] === "/api/nodes/n1/logs");
    expect(list[list.length - 1][1]).toMatchObject({ q: "typed" });
  });

  it("у узла без таблицы логов кнопки нет", async () => {
    mockServer();
    render(
      wrap(<LogsTab node={{ ...NODE, clickhouse_table: "" } as Node} initialFilter={{ q: "order_id" }} />),
    );

    await screen.findByText("logs.advanced.reset");
    expect(screen.queryByRole("button", { name: "logs.export.button" })).toBeNull();
  });
});
