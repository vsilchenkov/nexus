import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { OverviewTab } from "./OverviewTab";
import { type Node } from "../../api/client";
import { TooltipProvider } from "../ui";

// Карточка «Последние запросы» на вкладке «Обзор»: длинные значения обязаны
// обрезаться, а не распирать таблицу за край карточки.
//
// На прежнем коде таблица была с АВТО-раскладкой, и браузер игнорировал
// max-width у ячейки: подпуть §39 вида waInstance1103234744/getAvatar/<hash>
// растягивал колонку, и вся таблица уезжала вправо. Обрезка работает только с
// table-fixed, поэтому проверяется именно она.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const NODE = {
  id: "node-1",
  team_id: "team-a",
  path: "edo-sbis",
  root_method: "request",
  status: "active",
  clickhouse_table: "logs_edo",
  logging_enabled: true,
} as unknown as Node;

const LONG_METHOD = "waInstance1103234744/getAvatar/591a390ae706482a84730a0118e032405eaffd824e9b4befb2";
const LONG_URL =
  "https://api.green-api.com/waInstance1103234744/getAvatar/591a390ae706482a84730a0118e032405eaffd824e9b4befb2";

function mockServer() {
  apiGet.mockImplementation((url: string) => {
    if (url.includes("/logs")) {
      return Promise.resolve({
        items: [
          {
            id: "log-1",
            date_request: "2026-08-20T09:13:46Z",
            status: 400,
            duration_ms: 61,
            method: LONG_METHOD,
            url: LONG_URL,
            done: false,
          },
        ],
        logs_available: true,
      });
    }
    if (url.includes("/metrics")) {
      // KPI отдаём полностью: неполный объект роняет карточку на fmtNum
      // (undefined.toLocaleString) — тест зеленел бы при падающем рендере.
      return Promise.resolve({
        kpi: { total: 489, delivered: 447, errors: 42, p95_ms: 1600, p99_ms: 2000 },
        series: [],
      });
    }
    if (url === "/api/me/prefs") return Promise.resolve({ items: [] });
    if (url === "/api/me/teams") {
      return Promise.resolve({ items: [], current_team_id: "team-a", favorites: [] });
    }
    return Promise.resolve({});
  });
}

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0 } } });
  return render(
    <MemoryRouter>
      <TooltipProvider>
        <QueryClientProvider client={qc}>
          <OverviewTab node={NODE} />
        </QueryClientProvider>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe("OverviewTab: «Последние запросы» не уезжают за карточку", () => {
  beforeEach(() => {
    apiGet.mockReset();
    mockServer();
  });

  it("таблица с фиксированной раскладкой — иначе truncate не действует", async () => {
    const { container } = renderTab();
    await waitFor(() => expect(container.querySelector("table")).not.toBeNull());

    const table = container.querySelector("table");
    expect(table?.className).toContain("table-fixed");
    // Ширины колонок заданы явно: без colgroup у table-fixed их делит поровну.
    expect(container.querySelectorAll("colgroup col").length).toBe(5);
  });

  it("длинный метод обрезается и отдаёт полное значение подсказкой", async () => {
    const { container } = renderTab();
    const cell = await waitFor(() => {
      const el = container.querySelector(`td[title="${LONG_METHOD}"]`);
      expect(el).not.toBeNull();
      return el!;
    });
    expect(cell.className).toContain("truncate");
  });

  it("длинный URL показан ячейкой с подсказкой, как в журнале логов", async () => {
    const { container } = renderTab();
    await waitFor(() => {
      const spans = Array.from(container.querySelectorAll("span")).filter(
        (el) => el.textContent === LONG_URL,
      );
      expect(spans.length).toBeGreaterThan(0);
      // LogUrlCell обрезает значение сам — на нём класс truncate.
      expect(spans[0]?.className).toContain("truncate");
    });
  });
});

// §7.4 называет «Все логи» переходом, и адрес у него есть (?tab=logs). Значит
// ссылка, а не кнопка: у кнопки Ctrl+клик и «Открыть в новой вкладке» не
// работают — ровно так вкладки узла однажды получили адрес без ссылки (§79.3).
describe("OverviewTab: «Все логи» — ссылка, а не кнопка", () => {
  it("настоящая <a> с адресом вкладки логов", () => {
    renderTab();

    const link = screen.getByRole("link", { name: "metrics.all_logs" });
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href") ?? "").toContain("tab=logs");
    expect(screen.queryByRole("button", { name: "metrics.all_logs" })).toBeNull();
  });
});
