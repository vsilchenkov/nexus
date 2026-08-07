import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §84.9: пересчёт ответа шины по записи журнала. Отрендеренный ответ приёма не
// сохраняется нигде, поэтому блок показывает РАСЧЁТ по текущему шаблону — и
// обязан прямым текстом мешать принять его за факт.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { LogAckBlock } from "./LogAckBlock";
import { shouldShowAck } from "../../lib/logAckGate";
import { type AckSpec, type Node } from "../../api/client";

const SPEC: AckSpec = {
  version: 1,
  content_type: "application/json",
  body: '{"confirmedLogId": "${ body.logs[*].logId | max }"}',
  on_error: "error",
};

function node(root: Node["root_method"], spec: AckSpec | null): Node {
  return { id: "n1", path: "acs_sigur", root_method: root, async_ack_spec: spec } as unknown as Node;
}

const OK_RESP = {
  applicable: true,
  ok: true,
  status: 200,
  content_type: "application/json; charset=utf-8",
  body: '{"confirmedLogId": 79569}',
  on_error: "error",
  spec_changed_after_request: false,
  body_source: "stored",
  warnings: ["query_not_stored"],
};

function renderBlock(n: Node, recordType?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <LogAckBlock node={n} logId="log-1" recordType={recordType} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockResolvedValue(OK_RESP);
});

// Таблица условий показа §84.9.1. Каждая отсекающая строка проверяет и
// отсутствие блока, и ОТСУТСТВИЕ СЕТЕВОГО ЗАПРОСА.
describe("shouldShowAck (§84.9.1)", () => {
  it("у узла выключено изменение ответа → нет", () => {
    expect(shouldShowAck(node("requestAsync", null), "requestAsync")).toBe(false);
  });

  it("запись прошла синхронным путём → нет", () => {
    expect(shouldShowAck(node("request", SPEC), "request")).toBe(false);
  });

  // ГЛАВНАЯ строка: спека переживает перевод узла в sync и работает на паузе
  // (§83.5), поэтому у sync-узла бывают записи, чей ответ шаблон РЕАЛЬНО
  // формировал. На наивной реализации «гейт по root_method» этот кейс красный.
  it("узел снят с async, но запись асинхронная → ЕСТЬ", () => {
    expect(shouldShowAck(node("request", SPEC), "requestAsync")).toBe(true);
  });

  it("async-узел со спекой и async-записью → есть", () => {
    expect(shouldShowAck(node("requestAsync", SPEC), "requestAsync")).toBe(true);
  });

  it("тип записи неизвестен → нет (не угадываем)", () => {
    expect(shouldShowAck(node("requestAsync", SPEC), undefined)).toBe(false);
  });
});

describe("LogAckBlock (§84.9)", () => {
  it("у узла без спеки запрос не уходит вовсе", async () => {
    const { container } = renderBlock(node("requestAsync", null), "requestAsync");
    await Promise.resolve();
    expect(apiGet).not.toHaveBeenCalled();
    expect(container.textContent).toBe("");
  });

  it("у синхронной записи запрос не уходит вовсе", async () => {
    const { container } = renderBlock(node("request", SPEC), "request");
    await Promise.resolve();
    expect(apiGet).not.toHaveBeenCalled();
    expect(container.textContent).toBe("");
  });

  it("показывает пересчитанное тело и всегда предупреждает «сейчас, а не тогда»", async () => {
    renderBlock(node("requestAsync", SPEC), "requestAsync");
    await waitFor(() => expect(screen.getByText('{"confirmedLogId": 79569}')).toBeInTheDocument());
    // Главное предупреждение — безусловное, а не по подозрению.
    expect(screen.getByText(/logs\.detail\.ack\.warn_now_not_then/)).toBeInTheDocument();
    expect(screen.getByText(/logs\.detail\.ack\.warn_query_not_stored/)).toBeInTheDocument();
  });

  it("узел менялся после запроса → отдельное предупреждение", async () => {
    apiGet.mockResolvedValue({
      ...OK_RESP,
      spec_changed_after_request: true,
      spec_updated_at_ms: Date.parse("2026-08-06T21:40:00Z"),
      request_at_ms: Date.parse("2026-08-05T14:12:00Z"),
    });
    renderBlock(node("requestAsync", SPEC), "requestAsync");
    await waitFor(() =>
      expect(screen.getByText(/logs\.detail\.ack\.warn_spec_changed/)).toBeInTheDocument(),
    );
  });

  it("усечённое тело → предупреждение о том, что подстановка могла не найти значение", async () => {
    apiGet.mockResolvedValue({ ...OK_RESP, body_source: "truncated" });
    renderBlock(node("requestAsync", SPEC), "requestAsync");
    await waitFor(() =>
      expect(screen.getByText(/logs\.detail\.ack\.warn_truncated/)).toBeInTheDocument(),
    );
  });

  it("провал подстановки показывает причину и политику узла", async () => {
    apiGet.mockResolvedValue({
      applicable: true,
      ok: false,
      reason: "path_not_found",
      placeholder: "${ body.logs[*].logId | max }",
      on_error: "default",
      spec_changed_after_request: false,
    });
    renderBlock(node("requestAsync", SPEC), "requestAsync");
    await waitFor(() => expect(screen.getByText(/logs\.detail\.ack\.failed/)).toBeInTheDocument());
    expect(screen.getByText("${ body.logs[*].logId | max }")).toBeInTheDocument();
    // При on_error=default клиент получил бы обычный ответ — это другой исход,
    // и молчать о нём нельзя.
    expect(screen.getByText(/logs\.detail\.ack\.on_error_default/)).toBeInTheDocument();
  });

  it("сервер сказал applicable=false → блока нет (второй рубеж)", async () => {
    apiGet.mockResolvedValue({ applicable: false, ok: false, spec_changed_after_request: false });
    const { container } = renderBlock(node("requestAsync", SPEC), "requestAsync");
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
