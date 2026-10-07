import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import {
  disableConfirm,
  disablesQueue,
  hasAsyncQueue,
  pausesSyncNode,
  queueSnapshotOf,
  type NodeState,
} from "./statusConfirm";

// §113.11: правила подтверждения смены статуса. Тексты проверяются по ключам,
// число — по параметру интерполяции.

const t = ((k: string, o?: { waiting?: string }) =>
  o?.waiting !== undefined ? `${k}:${o.waiting}` : k) as unknown as TFunction;

const st = (root_method: string, status: NodeState["status"]): NodeState => ({ root_method, status });

describe("hasAsyncQueue — зеркало серверного usesAsyncQueue", () => {
  it.each([
    ["requestAsync", "enabled", true],
    ["RabbitMQAsync", "enabled", true],
    ["request", "enabled", false],
    ["request", "paused", true],
    ["request", "disabled", false],
  ] as const)("%s/%s → %s", (rm, status, want) => {
    expect(hasAsyncQueue(st(rm, status))).toBe(want);
  });
});

describe("pausesSyncNode", () => {
  it.each([
    ["sync: включён → пауза", st("request", "enabled"), st("request", "paused"), true],
    ["sync: отключён → пауза", st("request", "disabled"), st("request", "paused"), true],
    ["новый sync-узел сразу на паузе", undefined, st("request", "paused"), true],
    ["async на паузе стал sync на паузе", st("requestAsync", "paused"), st("request", "paused"), true],
    ["sync уже на паузе — не спрашиваем повторно", st("request", "paused"), st("request", "paused"), false],
    ["async: включён → пауза — без диалога", st("requestAsync", "enabled"), st("requestAsync", "paused"), false],
    ["pull: включён → пауза — без диалога", st("RabbitMQAsync", "enabled"), st("RabbitMQAsync", "paused"), false],
    ["sync: включён → отключён", st("request", "enabled"), st("request", "disabled"), false],
  ] as const)("%s", (_name, from, to, want) => {
    expect(pausesSyncNode(from, to)).toBe(want);
  });
});

describe("disablesQueue", () => {
  it.each([
    ["sync на паузе → отключён", st("request", "paused"), st("request", "disabled"), true],
    ["async включён → отключён", st("requestAsync", "enabled"), st("requestAsync", "disabled"), true],
    ["sync включён → отключён: очереди нет", st("request", "enabled"), st("request", "disabled"), false],
    ["уже отключён", st("requestAsync", "disabled"), st("requestAsync", "disabled"), false],
    ["async на паузу", st("requestAsync", "enabled"), st("requestAsync", "paused"), false],
    ["новый узел", undefined, st("requestAsync", "disabled"), false],
  ] as const)("%s", (_name, from, to, want) => {
    expect(disablesQueue(from, to)).toBe(want);
  });
});

describe("disableConfirm", () => {
  it("число ожидающих — в тексте, «+» при усечении", () => {
    expect(disableConfirm({ count: 3, capped: false }, t).message).toBe(
      "node.status_confirm.disable_queue_message:3",
    );
    expect(disableConfirm({ count: 50, capped: true }, t).message).toBe(
      "node.status_confirm.disable_queue_message:50+",
    );
  });

  it("пустая и неизвестная очередь различаются", () => {
    expect(disableConfirm({ count: 0, capped: false }, t).message).toBe(
      "node.status_confirm.disable_empty_message",
    );
    expect(disableConfirm(null, t).message).toBe("node.status_confirm.disable_unknown_message");
  });

  it("выключение — деструктивная кнопка", () => {
    expect(disableConfirm(null, t)).toMatchObject({
      title: "node.status_confirm.disable_title",
      confirmLabel: "node.status_confirm.disable_action",
      danger: true,
    });
  });
});

describe("queueSnapshotOf", () => {
  it("ответ без Kafka — неизвестно, а не ноль", () => {
    expect(queueSnapshotOf({ items: [], capped: false, kafka_available: false })).toBeNull();
    expect(queueSnapshotOf(undefined)).toBeNull();
  });

  it("число — длина списка, capped переносится", () => {
    expect(queueSnapshotOf({ items: [1, 2], capped: true, kafka_available: true })).toEqual({
      count: 2,
      capped: true,
    });
  });
});
