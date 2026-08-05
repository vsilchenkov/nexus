import { describe, expect, it } from "vitest";

import {
  isKnownNodeTab,
  parseLogsInitialFilter,
  parseNodeTab,
  withFailedLogs,
  withLogsWindow,
  withNodeTab,
} from "./nodeTabUrl";

// §79.3: вкладка узла и окно журнала живут в адресе. Здесь — контракт чистых
// функций: распознавание, сохранение ЧУЖИХ ключей (правило §54) и чистка окна
// журнала при уходе с вкладки «Логи».

describe("parseNodeTab", () => {
  it.each(["overview", "logs", "config", "metrics", "queue"])("распознаёт %s", (tab) => {
    expect(parseNodeTab(tab)).toBe(tab);
  });

  it("мусор и пусто → обзор", () => {
    expect(parseNodeTab("zzz")).toBe("overview");
    expect(parseNodeTab(null)).toBe("overview");
    expect(parseNodeTab("")).toBe("overview");
  });

  it("isKnownNodeTab отличает мусор от отсутствия — нормализуем только мусор", () => {
    expect(isKnownNodeTab("metrics")).toBe(true);
    expect(isKnownNodeTab("zzz")).toBe(false);
    expect(isKnownNodeTab(null)).toBe(false);
  });
});

describe("withNodeTab", () => {
  it("пишет вкладку и сохраняет чужие ключи", () => {
    const next = withNodeTab(new URLSearchParams("tab=logs&team=vika&foo=1"), "metrics");
    expect(next.get("tab")).toBe("metrics");
    expect(next.get("team")).toBe("vika");
    expect(next.get("foo")).toBe("1");
  });

  it("уход с логов чистит окно журнала — иначе адрес обещает окно, которого нет", () => {
    const prev = new URLSearchParams("tab=logs&from=100&to=200&status=err&done=no&team=vika");
    const next = withNodeTab(prev, "overview");
    expect(next.get("tab")).toBe("overview");
    expect(next.get("from")).toBeNull();
    expect(next.get("to")).toBeNull();
    expect(next.get("status")).toBeNull();
    expect(next.get("done")).toBeNull();
    expect(next.get("team")).toBe("vika");
  });

  it("возврат на логи окно не выдумывает", () => {
    const next = withNodeTab(new URLSearchParams("tab=overview"), "logs");
    expect(next.get("tab")).toBe("logs");
    expect(next.get("from")).toBeNull();
  });
});

describe("withLogsWindow", () => {
  it("клик по столбцу графика открывает логи за интервал", () => {
    const next = withLogsWindow(new URLSearchParams("tab=metrics&team=vika"), {
      from: 1000,
      to: 2000,
      onlyErrors: false,
    });
    expect(next.get("tab")).toBe("logs");
    expect(next.get("from")).toBe("1000");
    expect(next.get("to")).toBe("2000");
    expect(next.get("status")).toBeNull();
    expect(next.get("team")).toBe("vika");
  });

  it("клик по красному сегменту фильтрует ошибки", () => {
    const next = withLogsWindow(new URLSearchParams(), { from: 1, to: 2, onlyErrors: true });
    expect(next.get("status")).toBe("err");
  });
});

describe("withFailedLogs", () => {
  it("переход «Очередь → Логи» переводит даты в миллисекунды", () => {
    const from = "2026-08-05T10:00";
    const to = "2026-08-05T11:00";
    const next = withFailedLogs(new URLSearchParams("tab=queue"), { from, to, done: "no" });
    expect(next.get("tab")).toBe("logs");
    expect(next.get("from")).toBe(String(Date.parse(from)));
    expect(next.get("to")).toBe(String(Date.parse(to)));
    expect(next.get("done")).toBe("no");
  });
});

describe("parseLogsInitialFilter", () => {
  it("без окна и фильтров — null (LogsTab со своими дефолтами)", () => {
    expect(parseLogsInitialFilter(new URLSearchParams("tab=logs"))).toBeNull();
  });

  it("окно читается из миллисекунд", () => {
    const ms = Date.parse("2026-08-05T10:00:00.000Z");
    const f = parseLogsInitialFilter(new URLSearchParams(`from=${ms}&to=${ms + 3600_000}`));
    expect(f).not.toBeNull();
    expect(f?.from).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(f?.status).toBe("all");
  });

  it("done и status доезжают, мусорный status схлопывается в «все»", () => {
    const f = parseLogsInitialFilter(new URLSearchParams("done=no&status=zzz"));
    expect(f?.done).toBe("no");
    expect(f?.status).toBe("all");
  });

  it("битое окно не ломает разбор", () => {
    expect(parseLogsInitialFilter(new URLSearchParams("from=abc&to=def"))).toBeNull();
  });
});
