import { describe, expect, it } from "vitest";

import {
  isKnownNodeTab,
  parseLogsInitialFilter,
  parseMetricsView,
  parseNodeTab,
  shareableTabParams,
  withFailedLogs,
  withLogsWindow,
  withMetricsView,
  withNodeTab,
} from "./nodeTabUrl";
import { type Period } from "./period";

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

  // §84.2: правило обобщено с частного «уход с Логов чистит окно» до «остаются
  // только ключи целевой вкладки». На старом коде вид метрик выживал на любой
  // вкладке, и адрес обещал период, которого там нет.
  it("уход с метрик чистит период и шаг — красный на старом коде", () => {
    const prev = new URLSearchParams("tab=metrics&range=7d&step=6h&mfrom=1&mto=2&team=vika");
    const next = withNodeTab(prev, "logs");
    expect(next.get("tab")).toBe("logs");
    expect(next.get("range")).toBeNull();
    expect(next.get("step")).toBeNull();
    expect(next.get("mfrom")).toBeNull();
    expect(next.get("mto")).toBeNull();
    expect(next.get("team")).toBe("vika");
  });

  it("переход логи → метрики чистит окно журнала", () => {
    const next = withNodeTab(new URLSearchParams("tab=logs&from=1&to=2&status=err"), "metrics");
    expect(next.get("from")).toBeNull();
    expect(next.get("status")).toBeNull();
  });

  it("чужие ключи выживают при обоих переходах (правило §54)", () => {
    const prev = new URLSearchParams("tab=metrics&range=7d&utm_source=mail&team=vika");
    expect(withNodeTab(prev, "logs").get("utm_source")).toBe("mail");
    expect(withNodeTab(prev, "queue").get("team")).toBe("vika");
  });
});

describe("parseMetricsView (§84.2)", () => {
  it("пустой адрес — ничего не навязывает (дефолт добирает вызывающая сторона)", () => {
    expect(parseMetricsView(new URLSearchParams("tab=metrics"))).toEqual({});
  });

  it("пресет и шаг читаются", () => {
    const v = parseMetricsView(new URLSearchParams("range=7d&step=6h"));
    expect(v.period).toEqual({ kind: "preset", range: "7d" });
    expect(v.step).toBe("6h");
  });

  it("произвольный период читается из миллисекунд и выигрывает у пресета", () => {
    const from = Date.parse("2026-08-05T10:00:00.000Z");
    const to = from + 3_600_000;
    const v = parseMetricsView(new URLSearchParams(`range=7d&mfrom=${from}&mto=${to}`));
    expect(v.period).toEqual({
      kind: "custom",
      from: new Date(from).toISOString(),
      to: new Date(to).toISOString(),
    });
  });

  it("перевёрнутое и битое окно игнорируется, пресет остаётся в силе", () => {
    expect(parseMetricsView(new URLSearchParams("range=7d&mfrom=200&mto=100")).period).toEqual({
      kind: "preset",
      range: "7d",
    });
    expect(parseMetricsView(new URLSearchParams("range=7d&mfrom=abc&mto=def")).period).toEqual({
      kind: "preset",
      range: "7d",
    });
  });

  it("мусорные значения игнорируются молча", () => {
    const v = parseMetricsView(new URLSearchParams("range=zzz&step=99y"));
    expect(v.period).toBeUndefined();
    expect(v.step).toBeUndefined();
  });

  // 7d/14d/30d в словаре ЕСТЬ (вернули по требованию), а 5m/10m/2h убраны как
  // лишние кнопки — адрес с ними не должен молча применяться.
  it("убранные из пикера шаги в адресе не распознаются", () => {
    expect(parseMetricsView(new URLSearchParams("step=5m")).step).toBeUndefined();
    expect(parseMetricsView(new URLSearchParams("step=2h")).step).toBeUndefined();
    expect(parseMetricsView(new URLSearchParams("step=7d")).step).toBe("7d");
  });
});

describe("withMetricsView (§84.2)", () => {
  const p7d: Period = { kind: "preset", range: "7d" };
  const p24h: Period = { kind: "preset", range: "24h" };
  const defaults = { period: p24h };

  it("пишет вкладку, период и шаг; чужие ключи выживают", () => {
    const next = withMetricsView(new URLSearchParams("team=vika"), p7d, "6h", defaults);
    expect(next.get("tab")).toBe("metrics");
    expect(next.get("range")).toBe("7d");
    expect(next.get("step")).toBe("6h");
    expect(next.get("team")).toBe("vika");
  });

  it("дефолтный период в адрес не пишется — ссылка на него обязана быть короткой", () => {
    const next = withMetricsView(new URLSearchParams(), p24h, undefined, defaults);
    expect(next.get("range")).toBeNull();
    expect(next.get("step")).toBeNull();
    expect(next.get("tab")).toBe("metrics");
  });

  it("без известного дефолта период пишется всегда", () => {
    const next = withMetricsView(new URLSearchParams(), p24h, "1h");
    expect(next.get("range")).toBe("24h");
    expect(next.get("step")).toBe("1h");
  });

  // Ключевое различение §84.2: отсутствие ключа step = «шаг неявный».
  it("неявный шаг (undefined) в адрес не попадает вовсе", () => {
    const next = withMetricsView(new URLSearchParams("step=6h"), p7d, undefined, defaults);
    expect(next.get("range")).toBe("7d");
    expect(next.get("step")).toBeNull();
  });

  it("произвольный период уходит в mfrom/mto, а range стирается", () => {
    const from = Date.parse("2026-08-05T10:00:00.000Z");
    const to = from + 7_200_000;
    const custom: Period = { kind: "custom", from: new Date(from).toISOString(), to: new Date(to).toISOString() };
    const next = withMetricsView(new URLSearchParams("range=7d"), custom, "1h", defaults);
    expect(next.get("range")).toBeNull();
    expect(next.get("mfrom")).toBe(String(from));
    expect(next.get("mto")).toBe(String(to));
  });

  it("смена периода со своего на дефолтный убирает прежние ключи, а не копит их", () => {
    const prev = new URLSearchParams("tab=metrics&range=7d&step=6h");
    const next = withMetricsView(prev, p24h, undefined, defaults);
    expect(next.get("range")).toBeNull();
    expect(next.get("step")).toBeNull();
  });

  it("запись и чтение симметричны", () => {
    const written = withMetricsView(new URLSearchParams(), p7d, "6h", defaults);
    expect(parseMetricsView(written)).toEqual({ period: p7d, step: "6h" });
  });
});

describe("shareableTabParams (§84.2 + §79.3)", () => {
  it("ссылка на метрики несёт период и шаг", () => {
    expect(shareableTabParams("metrics")).toEqual(["range", "step", "mfrom", "mto"]);
  });

  it("окно журнала не шарится — это состояние клика по графику, а не то, чем делятся", () => {
    expect(shareableTabParams("logs")).toEqual([]);
    expect(shareableTabParams("overview")).toEqual([]);
    expect(shareableTabParams("queue")).toEqual([]);
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

  // §84.2: правило владения ключами одно на все три перехода. Раньше чистку
  // делал только withNodeTab, и клик по столбцу графика оставлял период с
  // шагом в адресе — тот обещал вид вкладки, на которой пользователя уже нет.
  it("клик по столбцу уносит с метрик и чистит их ключи — красный на старом коде", () => {
    const prev = new URLSearchParams("tab=metrics&range=7d&step=6h&team=vika");
    const next = withLogsWindow(prev, { from: 1, to: 2, onlyErrors: false });
    expect(next.get("tab")).toBe("logs");
    expect(next.get("range")).toBeNull();
    expect(next.get("step")).toBeNull();
    expect(next.get("team")).toBe("vika");
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

  it("переход из «Очереди» тоже чистит ключи чужих вкладок", () => {
    const prev = new URLSearchParams("tab=queue&range=7d&step=6h");
    const next = withFailedLogs(prev, { from: "", to: "", done: "no" });
    expect(next.get("range")).toBeNull();
    expect(next.get("step")).toBeNull();
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
