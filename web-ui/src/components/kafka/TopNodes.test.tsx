import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { TopNodes } from "./TopNodes";
import type { KafkaByNode } from "../../api/client";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Период экрана Kafka: ровно он должен уехать в адрес журнала.
const FROM = 1_757_000_000_000;
const TO = 1_757_003_600_000;

const DATA: KafkaByNode = {
  top_producers: [
    { node_path: "vika/telephony", node_id: "n-1", produced: 12400, share: 0.41 },
    { node_path: "<unresolved>", produced: 820, share: 0.03 },
  ],
  top_failures: [{ node_path: "conv/dconv", node_id: "n-2", failed: 318, rate: 0.04 }],
  prometheus_available: true,
};

function renderTop() {
  render(
    <MemoryRouter>
      <TopNodes data={DATA} from={FROM} to={TO} />
    </MemoryRouter>,
  );
}

describe("TopNodes — узлы ведут на журнал (§98.2)", () => {
  // §79: проверяется именно ПРИРОДА элемента. Обработчик клика прошёл бы этот
  // сценарий и всё равно оставил бы Ctrl+клик и «Открыть в новой вкладке»
  // неработающими — ровно так вкладки узла и получили адрес без ссылки.
  it("узел с id — настоящая ссылка (<a href>), а не кнопка", () => {
    renderTop();
    const link = screen.getByRole("link", { name: "vika/telephony" });
    expect(link.tagName).toBe("A");
    expect(link).toHaveAttribute("href");
  });

  it("ссылка ведёт на вкладку «Логи» узла в периоде экрана Kafka", () => {
    renderTop();
    const href = screen.getByRole("link", { name: "vika/telephony" }).getAttribute("href") ?? "";
    const [path, query] = href.split("?");
    const params = new URLSearchParams(query);

    expect(path).toBe("/nodes/n-1");
    expect(params.get("tab")).toBe("logs");
    expect(params.get("from")).toBe(String(FROM));
    expect(params.get("to")).toBe(String(TO));
    // Журнал открывается целиком, а не только ошибками: всплеск виден лишь
    // рядом с успешными доставками.
    expect(params.get("status")).toBeNull();
  });

  it("блок ошибок ссылается так же", () => {
    renderTop();
    const href = screen.getByRole("link", { name: "conv/dconv" }).getAttribute("href") ?? "";
    expect(href.startsWith("/nodes/n-2?")).toBe(true);
  });

  it("путь без id остаётся текстом — ссылки «в никуда» быть не должно", () => {
    renderTop();
    expect(screen.getByText("<unresolved>")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "<unresolved>" })).toBeNull();
  });
});
