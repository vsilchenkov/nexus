import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { TopicsTable } from "./TopicsTable";
import type { KafkaTopic, KafkaTopicGroup } from "../../api/client";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function topic(name: string, groups: KafkaTopicGroup[]): KafkaTopic {
  return {
    name,
    partitions: 4,
    replication_factor: 1,
    size_bytes: 0,
    messages_estimate: 0,
    retention_ms: 0,
    consumer_groups: groups,
    under_replicated: 0,
    offline_partitions: 0,
  };
}

function row(name: string): HTMLElement {
  return screen.getByText(name).closest("tr") as HTMLElement;
}

describe("TopicsTable — роли групп Nexus (§113.7)", () => {
  // Инцидент 06.10: очередь узла на паузе красила nexus.async.paused в
  // «растущий lag», хотя сообщения ждали по замыслу.
  it("lag очереди паузы и DLQ не делает топик «растущим lag»", () => {
    render(
      <TopicsTable
        sizesAvailable
        kafkaAvailable
        topics={[
          topic("nexus.async.paused", [{ group: "nexus-sender-paused", lag_total: 6_600_000, members: 1, role: "paused" }]),
          topic("nexus.async.dlq", [{ group: "nexus-sender-dlq-reprocess", lag_total: 5000, members: 1, role: "dlq" }]),
          topic("nexus.async", [{ group: "nexus-sender", lag_total: 5000, members: 2, role: "main" }]),
        ]}
      />,
    );
    expect(within(row("nexus.async.paused")).getByText("kafka.topics.state.ok")).toBeInTheDocument();
    expect(within(row("nexus.async.dlq")).getByText("kafka.topics.state.ok")).toBeInTheDocument();
    expect(within(row("nexus.async")).getByText("kafka.topics.state.lag")).toBeInTheDocument();
  });

  it("плашка группы паузы — «отложено», с подсказкой, без lag", () => {
    render(
      <TopicsTable
        sizesAvailable
        kafkaAvailable
        topics={[topic("nexus.async.paused", [{ group: "nexus-sender-paused", lag_total: 960, members: 1, role: "paused" }])]}
      />,
    );
    const chip = screen.getByText("kafka.topics.role_waiting.paused");
    expect(chip.closest("[title]")).toHaveAttribute("title", "kafka.topics.role_hint.paused");
    expect(screen.queryByText(/· lag/)).toBeNull();
  });

  it("посторонняя группа без роли — как раньше, lag в тоне", () => {
    render(
      <TopicsTable
        sizesAvailable
        kafkaAvailable
        topics={[topic("orders", [{ group: "debug-tool", lag_total: 2000, members: 1 }])]}
      />,
    );
    expect(screen.getByText(/^debug-tool · lag 2/)).toBeInTheDocument();
    expect(within(row("orders")).getByText("kafka.topics.state.lag")).toBeInTheDocument();
  });
});

describe("TopicsTable — пустой список (§114.5)", () => {
  it("брокер ответил, топиков нет — «Топиков нет»", () => {
    render(<TopicsTable sizesAvailable kafkaAvailable topics={[]} />);
    expect(screen.getByText("kafka.topics.empty")).toBeInTheDocument();
  });

  it("брокер не ответил — «список недоступен», а не «топиков нет»", () => {
    render(<TopicsTable sizesAvailable={false} kafkaAvailable={false} topics={[]} />);
    expect(screen.getByText("kafka.topics.unavailable")).toBeInTheDocument();
    expect(screen.queryByText("kafka.topics.empty")).toBeNull();
  });
});

describe("TopicsTable — посторонняя группа без commit (§114.6)", () => {
  it("число партиций без commit — на плашке, причина — в подсказке", () => {
    render(
      <TopicsTable
        sizesAvailable
        kafkaAvailable
        topics={[topic("nexus.async", [{ group: "debug-tool", lag_total: 12, members: 0, uncommitted_partitions: 3 }])]}
      />,
    );
    const chip = screen.getByText(/debug-tool · lag 12/);
    expect(chip).toHaveTextContent("kafka.topics.uncommitted");
    expect(chip.closest("span[title]")).toHaveAttribute("title", "kafka.topics.uncommitted_hint");
  });

  it("все партиции с commit — плашка как раньше, без подсказки", () => {
    render(
      <TopicsTable
        sizesAvailable
        kafkaAvailable
        topics={[topic("nexus.async", [{ group: "debug-tool", lag_total: 12, members: 0 }])]}
      />,
    );
    const chip = screen.getByText(/debug-tool · lag 12/);
    expect(chip).not.toHaveTextContent("kafka.topics.uncommitted");
    expect(chip.closest("span[title]")).toBeNull();
  });
});
