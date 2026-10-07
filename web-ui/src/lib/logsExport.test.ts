import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";

import {
  EXPORT_ROW_META_BYTES,
  exportErrorText,
  exportFilterSummary,
  exportParams,
  exportPlan,
  exportRowChoices,
  exportRowLimit,
  isExportCanceled,
  type LogsExportEstimate,
} from "./logsExport";
import { emptyAdvForm, type LogsFilterState } from "./logsQuery";

// §115.4: чистые функции формы выгрузки журнала.

const t = ((k: string, o?: Record<string, unknown>) =>
  o ? `${k}:${JSON.stringify(o)}` : k) as unknown as TFunction;

const EST: LogsExportEstimate = {
  records: 1000,
  rows: 2000,
  request_bytes: 2000 * 600,
  response_bytes: 0,
  max_rows: 50_000,
  max_bytes: 100 << 20,
  max_body_chars: 32_767,
  logs_configured: true,
  logs_available: true,
};

const FILTER: LogsFilterState = { ...emptyAdvForm, status: "all", done: "all" };

describe("exportRowLimit", () => {
  it.each([
    { name: "пресет ниже потолка", choice: "1000" as const, max: 50_000, want: 1000 },
    { name: "пресет выше потолка урезается", choice: "10000" as const, max: 5000, want: 5000 },
    { name: "все = потолок сервера", choice: "max" as const, max: 50_000, want: 50_000 },
    { name: "потолок неизвестен: пресет как есть", choice: "10000" as const, max: 0, want: 10_000 },
    { name: "потолок неизвестен: все = 0 (решает сервер)", choice: "max" as const, max: 0, want: 0 },
  ])("$name", ({ choice, max, want }) => {
    expect(exportRowLimit(choice, max)).toBe(want);
  });
});

describe("exportRowChoices", () => {
  it("пресеты не меньше потолка не дублируют «все»", () => {
    expect(exportRowChoices(50_000)).toEqual(["1000", "10000", "max"]);
    expect(exportRowChoices(10_000)).toEqual(["1000", "max"]);
    expect(exportRowChoices(500)).toEqual(["max"]);
  });
});

describe("exportPlan", () => {
  it("тела считаются по среднему на прогон, без упора в лимиты", () => {
    const p = exportPlan(EST, 50_000, true);
    expect(p.inFile).toBe(1000);
    expect(p.bytes).toBe(1000 * (EXPORT_ROW_META_BYTES + 600));
    expect(p.limitedByRows).toBe(false);
    expect(p.limitedByBytes).toBe(false);
  });

  it("без тел объём — только остальные колонки", () => {
    expect(exportPlan(EST, 50_000, false).bytes).toBe(1000 * EXPORT_ROW_META_BYTES);
  });

  it("предел строк меньше найденного", () => {
    const p = exportPlan(EST, 100, true);
    expect(p.inFile).toBe(100);
    expect(p.limitedByRows).toBe(true);
  });

  it("оценка выше лимита объёма — только предупреждение, без числа записей", () => {
    const p = exportPlan({ ...EST, max_bytes: 100_000 }, 50_000, true);
    expect(p.limitedByBytes).toBe(true);
    expect(p).not.toHaveProperty("fitRows");
  });

  it("пустой журнал не делит на ноль", () => {
    const p = exportPlan({ ...EST, records: 0, rows: 0, request_bytes: 0 }, 50_000, true);
    expect(p).toEqual({ inFile: 0, bytes: 0, limitedByRows: false, limitedByBytes: false });
  });
});

describe("exportParams", () => {
  it("фильтр журнала + предел, тела и пояс", () => {
    const f: LogsFilterState = { ...FILTER, q: "order", qRegex: true, status: "err", done: "pending" };
    expect(exportParams(f, { rows: 1000, bodies: true, tz: "Europe/Moscow" })).toEqual({
      q: "order",
      q_regex: "1",
      status: "err",
      done: "no",
      rows: "1000",
      bodies: "1",
      tz: "Europe/Moscow",
    });
  });

  it("без тел, без пояса и с неизвестным потолком — параметров нет", () => {
    expect(exportParams(FILTER, { rows: 0, bodies: false, tz: "" })).toEqual({});
  });
});

describe("exportErrorText", () => {
  const err = (status: number, error?: string) => ({ response: { status, data: error ? { error } : {} } });

  it.each([
    { name: "400 — синтаксис поиска", e: err(400, "x"), want: "logs.advanced.bad_query" },
    { name: "404 — свой текст вместо голого «not found»", e: err(404, "not found"), want: "logs.export.err_not_found" },
    { name: "500 — текст сервера (ClickHouse)", e: err(500, "ClickHouse недоступен"), want: "ClickHouse недоступен" },
    { name: "429 — частота", e: err(429, "rate limited"), want: "logs.export.err_rate" },
    { name: "503 — текст сервера (занято/ClickHouse)", e: err(503, "Все слоты заняты"), want: "Все слоты заняты" },
    { name: "503 без текста", e: err(503), want: "logs.export.err_generic" },
    { name: "сеть", e: new Error("Network Error"), want: "logs.export.err_generic" },
  ])("$name", ({ e, want }) => {
    expect(exportErrorText(e, t)).toBe(want);
  });
});

describe("isExportCanceled", () => {
  it("только отмена axios", () => {
    expect(isExportCanceled({ code: "ERR_CANCELED" })).toBe(true);
    expect(isExportCanceled({ code: "ERR_NETWORK" })).toBe(false);
    expect(isExportCanceled(null)).toBe(false);
  });
});

describe("exportFilterSummary", () => {
  it("пустой фильтр — пустой список (форма пишет «Фильтры не установлены»)", () => {
    expect(exportFilterSummary(FILTER, t)).toEqual([]);
  });

  it("показываются только заданные фильтры", () => {
    const f: LogsFilterState = { ...FILTER, status: "ok", clientHost: "client.local" };
    expect(exportFilterSummary(f, t)).toEqual([
      { label: "logs.export.state", value: "logs.filter.ok" },
      { label: "logs.advanced.client", value: "client.local" },
    ]);
  });

  it("заданные фильтры — значения, режимы поиска и прогон из дип-линка", () => {
    const f: LogsFilterState = {
      ...FILTER,
      q: "order_id",
      qCase: true,
      qRegex: true,
      from: "2026-10-07T00:00",
      to: "2026-10-07T23:59",
      status: "err",
      method: "orders.create",
      clientIp: "10.0.0.5",
      done: "pending",
    };
    const v = Object.fromEntries(exportFilterSummary(f, t).map((i) => [i.label, i.value]));
    expect(v).toEqual({
      "logs.advanced.q": "order_id  [Aa .*]",
      "logs.export.period": "07.10.2026 00:00 — 07.10.2026 23:59",
      "logs.export.state": "logs.filter.err",
      "logs.advanced.method": "orders.create",
      "logs.advanced.client": "10.0.0.5",
      "logs.export.run": "logs.filter.done_no",
    });
  });

  it("одна граница периода", () => {
    const [from] = exportFilterSummary({ ...FILTER, from: "2026-10-07T08:30" }, t);
    const [to] = exportFilterSummary({ ...FILTER, to: "2026-10-07T08:30" }, t);
    expect(from.value).toBe('logs.export.period_from:{"from":"07.10.2026 08:30"}');
    expect(to.value).toBe('logs.export.period_to:{"to":"07.10.2026 08:30"}');
  });
});
