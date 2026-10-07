import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { HealthBanner } from "./HealthBanner";
import type { KafkaOverview, KafkaSeverity } from "../../api/client";
import en from "../../locales/en.json";
import ru from "../../locales/ru.json";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// Reason-коды баннера с бэкенда (internal/web/usecase/kafka_health.go). Текст
// локализует фронт: код без ключа показал бы оператору сырой путь ключа.
const REASONS = [
  "healthy",
  "offline_partitions",
  "brokers_down",
  "error_rate_high",
  "lag_high",
  "lag_growing",
  "under_replicated",
  "produce_latency_high",
  "broker_unreachable",
];

function overview(severity: KafkaSeverity, reason: string): KafkaOverview {
  return {
    period: { from: "", to: "" },
    summary: {
      produced_total: 0,
      consumed_total: 0,
      failed_produced: 0,
      failed_consumed: 0,
      current_lag: 0,
      in_flight_now: 0,
      paused_backlog: 0,
    },
    delta_vs_previous_period: { produced: 0, consumed: 0, has_delta: false },
    error_rate: 0,
    broker_health: { brokers_total: 0, brokers_online: 0, under_replicated_partitions: 0, offline_partitions: 0 },
    health: { severity, reason },
    prometheus_available: true,
    kafka_available: false,
    paused_backlog_available: false,
    lag_from_broker: false,
    paused_nodes: [],
    paused_nodes_total: 0,
    paused_nodes_available: false,
  };
}

describe("HealthBanner — брокер не отвечает (§114.5)", () => {
  it("broker_unreachable — жёлтый баннер со своим текстом", () => {
    const { container } = render(<HealthBanner ov={overview("warn", "broker_unreachable")} />);
    expect(screen.getByText("kafka.health.broker_unreachable.title")).toBeInTheDocument();
    expect(screen.getByText("kafka.health.broker_unreachable.details")).toBeInTheDocument();
    expect((container.firstChild as HTMLElement).className).toContain("text-warn");
  });

  it.each(REASONS)("у причины %s есть заголовок и текст в обеих локалях", (reason) => {
    for (const loc of [en, ru]) {
      const h = (loc.kafka.health as Record<string, { title?: string; details?: string }>)[reason];
      expect(h?.title, reason).toBeTruthy();
      expect(h?.details, reason).toBeTruthy();
    }
  });
});
