import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { PausedBacklog } from "./PausedBacklog";
import type { KafkaOverview, KafkaPausedNode } from "../../api/client";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function overview(over: {
  backlog?: number;
  nodes?: KafkaPausedNode[];
  total?: number;
  available?: boolean;
  backlogAvailable?: boolean;
}): KafkaOverview {
  const nodes = over.nodes ?? [];
  return {
    period: { from: "", to: "" },
    summary: {
      produced_total: 0,
      consumed_total: 0,
      failed_produced: 0,
      failed_consumed: 0,
      current_lag: 0,
      in_flight_now: 0,
      paused_backlog: over.backlog ?? 0,
    },
    delta_vs_previous_period: { produced: 0, consumed: 0, has_delta: false },
    error_rate: 0,
    broker_health: { brokers_total: 1, brokers_online: 1, under_replicated_partitions: 0, offline_partitions: 0 },
    health: { severity: "ok", reason: "" },
    prometheus_available: true,
    kafka_available: true,
    paused_backlog_available: over.backlogAvailable ?? true,
    lag_from_broker: true,
    paused_nodes: nodes,
    paused_nodes_total: over.total ?? nodes.length,
    paused_nodes_available: over.available ?? true,
  };
}

function renderLine(ov: KafkaOverview) {
  return render(
    <MemoryRouter>
      <PausedBacklog ov={ov} />
    </MemoryRouter>,
  );
}

const SBIS: KafkaPausedNode = { id: "n-1", team: "edo", path: "vika_dev-epd_sbis_pp" };

describe("PausedBacklog — очередь узлов на паузе в карточке lag (§113.7)", () => {
  it("пустая очередь — строки нет", () => {
    const { container } = renderLine(overview({ backlog: 0, nodes: [SBIS] }));
    expect(container).toBeEmptyDOMElement();
  });

  it("очередь с узлом: число и раскрывающийся список узлов", () => {
    renderLine(overview({ backlog: 960, nodes: [SBIS] }));
    expect(screen.getByText("kafka.paused.backlog")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();

    const toggle = screen.getByRole("button", { name: /kafka.paused.nodes/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link")).toBeNull();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
  });

  // §79: проверяется природа элемента — ссылка, а не кнопка, иначе Ctrl+клик и
  // «Открыть в новой вкладке» не работают.
  it("узел ведёт на свою вкладку «Очередь» настоящей ссылкой", () => {
    renderLine(overview({ backlog: 960, nodes: [SBIS] }));
    fireEvent.click(screen.getByRole("button", { name: /kafka.paused.nodes/ }));

    const link = screen.getByRole("link", { name: "edo / vika_dev-epd_sbis_pp" });
    expect(link.tagName).toBe("A");
    const [path, query] = (link.getAttribute("href") ?? "").split("?");
    expect(path).toBe("/nodes/n-1");
    expect(new URLSearchParams(query).get("tab")).toBe("queue");
  });

  it("узлов больше, чем в списке, — «и ещё N»", () => {
    renderLine(overview({ backlog: 5, nodes: [SBIS], total: 3 }));
    fireEvent.click(screen.getByRole("button", { name: /kafka.paused.nodes/ }));
    expect(screen.getByText("kafka.paused.more")).toBeInTheDocument();
  });

  it("очередь есть, а узлов на паузе нет — предупреждение без списка", () => {
    renderLine(overview({ backlog: 960, nodes: [], total: 0 }));
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("kafka.paused.stuck");
    expect(status.className).toContain("text-warn");
    expect(screen.queryByRole("button")).toBeNull();
  });

  // Список не прочитан (ошибка PostgreSQL): «узлов на паузе нет» утверждать
  // нельзя — только число, без предупреждения и без списка.
  it("список узлов недоступен — только число", () => {
    renderLine(overview({ backlog: 960, nodes: [], total: 0, available: false }));
    expect(screen.getByText("kafka.paused.backlog")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });
});

describe("PausedBacklog — брокер не ответил (§114.5)", () => {
  // paused_backlog=0 при непрочитанной очереди не значит «пусто»: строка есть
  // всегда, приглушённая, с подсказкой — а не пустота, как у пустой очереди.
  it("очередь не прочитана — «отложено: —» с подсказкой, даже при нуле", () => {
    renderLine(overview({ backlog: 0, backlogAvailable: false }));
    const line = screen.getByText("kafka.paused.unavailable").closest("div") as HTMLElement;
    expect(line).toHaveAttribute("title", "kafka.paused.unavailable_hint");
    expect(line.className).toContain("text-fg-subtle");
    expect(screen.queryByText("kafka.paused.backlog")).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
