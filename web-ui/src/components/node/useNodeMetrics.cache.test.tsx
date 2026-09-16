import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §102: «Обзор» и «Метрики» зовут ОДИН эндпоинт с одним id, периодом и пустыми
// фильтрами. Разводит их только флаг with_methods, поэтому он обязан входить и
// в queryKey, и в ключ useStableData. Без этого две вкладки делят запись кеша:
// поллинг «Обзора» (ответ без methods) затирает разбивку, и карточка «Запросы
// по методам» мигает «недоступно» на живых данных — та же грабля, что §72.2.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

import { useNodeMetrics } from "./useNodeMetrics";
import { type NodeMetricsResp } from "../../api/client";
import { type Period } from "../../lib/period";

const PERIOD: Period = { kind: "preset", range: "1h" };

const BASE: NodeMetricsResp = {
  kpi: { total: 12, delivered: 12, errors: 0, p95_ms: 10, p99_ms: 20 },
  series: [{ ts: 1, count: 12, errors: 0 }],
  chart_available: true,
  range_ms: 3600_000,
};

const WITH_METHODS: NodeMetricsResp = {
  ...BASE,
  methods: [{ method: "v1/suggest", total: 12, delivered: 12 }],
  methods_available: true,
  methods_truncated: false,
};

function metricsCalls(): Record<string, string>[] {
  return apiGet.mock.calls
    .filter((c) => String(c[0]).startsWith("/api/metrics/nodes/"))
    .map((c) => (c[1] ?? {}) as Record<string, string>);
}

// Обе вкладки живут в ОДНОМ QueryClient — иначе тест проверял бы не кеш, а
// изоляцию клиентов, и разделения ключей не потребовалось бы вовсе.
function wrapper(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockImplementation((url: string, params?: Record<string, string>) => {
    if (url === "/api/settings/public") return Promise.resolve({ metrics_refetch_ms: 999_000 });
    if (url.startsWith("/api/metrics/nodes/")) {
      return Promise.resolve(params?.with_methods === "1" ? WITH_METHODS : BASE);
    }
    return Promise.resolve({});
  });
});

describe("useNodeMetrics: разбивка по методам не делит кеш с «Обзором» (§102)", () => {
  it("with_methods=1 уходит только от запросившей вкладки", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const w = wrapper(qc);

    const overview = renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, false), {
      wrapper: w,
    });
    const metrics = renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, true), {
      wrapper: w,
    });

    await waitFor(() => expect(metricsCalls()).toHaveLength(2));
    const flags = metricsCalls().map((p) => p.with_methods);
    expect(flags).toContain("1");
    expect(flags).toContain(undefined);

    await waitFor(() => expect(metrics.result.current.data?.methods).toHaveLength(1));
    // Ответ «Обзора» разбивку не несёт и НЕ должен её отобрать.
    expect(overview.result.current.data?.methods).toBeUndefined();
    expect(metrics.result.current.data?.methods_available).toBe(true);
  });

  it("поллинг «Обзора» не затирает разбивку «Метрик»", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const w = wrapper(qc);

    const metrics = renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, true), {
      wrapper: w,
    });
    await waitFor(() => expect(metrics.result.current.data?.methods).toHaveLength(1));

    renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, false), { wrapper: w });
    await waitFor(() => expect(metricsCalls()).toHaveLength(2));
    // Ответ без разбивки приехал в СВОЮ запись кеша — карточка метрик цела.
    await waitFor(() => expect(metrics.result.current.data?.methods).toHaveLength(1));
  });

  it("запись кеша у флага своя: у «Обзора» и «Метрик» разные ключи", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const w = wrapper(qc);

    renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, false), { wrapper: w });
    renderHook(() => useNodeMetrics("n1", PERIOD, "auto", {}, true, true), { wrapper: w });

    await waitFor(() => expect(metricsCalls()).toHaveLength(2));
    const keys = qc
      .getQueryCache()
      .getAll()
      .map((q) => q.queryKey)
      .filter((k) => k[0] === "node-metrics");
    expect(keys).toHaveLength(2);
    expect(keys.map((k) => k.at(-1)).sort()).toEqual([false, true]);
  });
});
