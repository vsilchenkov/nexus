import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TopNodes } from "./TopNodes";
import type { KafkaByNode } from "../../api/client";
import { expectStretchedTo } from "../../test/stretched";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Период экрана Kafka: ровно он должен уехать в адрес журнала.
const FROM = 1_757_000_000_000;
const TO = 1_757_003_600_000;

// Боевой случай §101: путь telegram есть у двух команд, плюс ряды, собранные до
// разделения метрик по командам, которые сервер не смог приписать узлу.
const DATA: KafkaByNode = {
  top_producers: [
    { node_path: "vika/telephony", team: "vika", node_id: "n-1", produced: 12400, share: 0.41 },
    { node_path: "<unresolved>", produced: 820, share: 0.03 },
  ],
  top_failures: [
    { node_path: "telegram", team: "messengers", node_id: "tg-m", failed: 1747, rate: 0.195 },
    { node_path: "telegram", team: "webhook", node_id: "tg-w", failed: 8, rate: 0.035 },
    { node_path: "telegram", failed: 412, rate: 0.021 },
    { node_path: "conv/old", team: "conv", failed: 5, rate: 0.01 },
  ],
  prometheus_available: true,
};

function renderTop(data: KafkaByNode = DATA) {
  render(
    <MemoryRouter>
      <TopNodes data={data} from={FROM} to={TO} />
    </MemoryRouter>,
  );
}

// linkTo — ссылка на узел по id: имена ссылок одного пути различает только
// слог команды, а адрес однозначен всегда.
function linkTo(id: string): HTMLElement {
  const link = screen
    .getAllByRole("link")
    .find((a) => (a.getAttribute("href") ?? "").startsWith(`/nodes/${id}?`));
  expect(link, `ссылка на /nodes/${id}`).toBeTruthy();
  return link!;
}

// textCell — ячейка строки без ссылки, та, что несёт подсказку.
function textCell(text: string): HTMLElement {
  return screen.getByText(text).closest("span[title]") as HTMLElement;
}

describe("TopNodes — узлы ведут на журнал (§98.2)", () => {
  // §79: проверяется именно ПРИРОДА элемента. Обработчик клика прошёл бы этот
  // сценарий и всё равно оставил бы Ctrl+клик и «Открыть в новой вкладке»
  // неработающими — ровно так вкладки узла и получили адрес без ссылки.
  it("узел с id — настоящая ссылка (<a href>), а не кнопка", () => {
    renderTop();
    const link = screen.getByRole("link", { name: "vika/telephony vika" });
    expect(link.tagName).toBe("A");
    expect(link).toHaveAttribute("href");
  });

  it("ссылка ведёт на вкладку «Логи» узла в периоде экрана Kafka", () => {
    renderTop();
    const href = linkTo("n-1").getAttribute("href") ?? "";
    const [path, query] = href.split("?");
    const params = new URLSearchParams(query);

    expect(path).toBe("/nodes/n-1");
    expect(params.get("tab")).toBe("logs");
    expect(params.get("from")).toBe(String(FROM));
    expect(params.get("to")).toBe(String(TO));
    // Журнал открывается целиком, а не только ошибками: всплеск виден лишь
    // рядом с успешными доставками.
    expect(params.get("status")).toBeNull();
    // Команду переключает страница узла по самому узлу (§58), а не адрес.
    expect(params.get("team")).toBeNull();
  });

  it("путь без id остаётся текстом — ссылки «в никуда» быть не должно", () => {
    renderTop();
    expect(screen.getByText("<unresolved>")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /<unresolved>/ })).toBeNull();
  });

  // Молчаливое отсутствие ссылки читается как «ссылки не работают» — так и было
  // доложено со стенда. Причина в данных, и строка обязана её называть.
  it("строка без ссылки объясняет, почему её нет", () => {
    renderTop();
    expect(textCell("<unresolved>")).toHaveAttribute("title", "kafka.node_placeholder");
    // У рабочей ссылки такого пояснения быть не должно.
    expect(linkTo("n-1")).not.toHaveAttribute("title");
  });
});

// §101.10: строка — пара (команда, путь). На бою путь telegram был у двух
// команд, метрики складывали их в одну строку, и ссылки у неё не было.
describe("TopNodes — одноимённые узлы разных команд (§101.10)", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("один путь в двух командах — две ссылки, каждая на свой узел", () => {
    renderTop();

    const messengers = linkTo("tg-m");
    const webhook = linkTo("tg-w");
    expect(within(messengers).getByText("messengers")).toBeInTheDocument();
    expect(within(webhook).getByText("webhook")).toBeInTheDocument();
    // Доступные имена различаются: иначе скринридер читал бы две одинаковые
    // ссылки «telegram».
    expect(messengers).toHaveAccessibleName("telegram messengers");
    expect(webhook).toHaveAccessibleName("telegram webhook");
  });

  // Ключ по одному пути у одноимённых строк совпадает: React молча
  // переиспользует DOM чужой строки, и при обновлении числа и ссылки
  // перепутываются. Единственный видимый в тесте след — предупреждение.
  it("ключи строк уникальны", () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    renderTop();

    const dupKey = errors.mock.calls.filter((args) => args.join(" ").includes("same key"));
    expect(dupKey).toEqual([]);
  });

  it("ряды до разделения по командам — текст с чипом «без команды» и своей подсказкой", () => {
    renderTop();

    const cell = textCell("kafka.no_team");
    expect(cell).toHaveTextContent("telegram");
    expect(cell).toHaveAttribute("title", "kafka.node_legacy");
    expect(cell.closest("a")).toBeNull();
  });

  it("узла с этой парой нет — текст с чипом команды и подсказкой «переименован, удалён, перенесён»", () => {
    renderTop();

    const cell = textCell("conv/old");
    expect(within(cell).getByText("conv")).toBeInTheDocument();
    expect(cell).toHaveAttribute("title", "kafka.node_unresolved");
  });

  // У заглушки команды не бывает: чип «без команды» рядом с ней читался бы как
  // ещё одна строка до разделения по командам.
  it("у заглушки <unresolved> чипа нет", () => {
    renderTop();

    expect(textCell("<unresolved>")).toHaveTextContent(/^<unresolved>$/);
  });
});

// §7.3 (тот же дефект, что был на «Узлах»): строка подсвечивалась целиком, а
// кликалась одна колонка из трёх — клик мимо надписи не делал ничего.
describe("TopNodes — зона клика по строке", () => {
  it("ссылка растянута по всей строке в обоих топах", () => {
    renderTop();

    for (const id of ["n-1", "tg-m", "tg-w"]) {
      const link = linkTo(id);
      expectStretchedTo(link, link.closest("tr") as HTMLElement);
    }
  });

  // §98.2: путь без узла ссылкой не становится. Значит и подсвечивать его как
  // кликабельный нельзя — это ровно то враньё, которое §98.2 просил убрать.
  it("строка без ссылки не подсвечивается и не служит опорой", () => {
    renderTop();

    for (const text of ["<unresolved>", "kafka.no_team", "conv/old"]) {
      const row = screen.getByText(text).closest("tr") as HTMLElement;
      expect(row.className).not.toMatch(/hover:bg-bg-muted/);
      expect(row).not.toHaveClass("relative");
    }
  });
});
