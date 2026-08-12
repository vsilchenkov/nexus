import { describe, expect, it } from "vitest";

import { ackFormDefaults, ackFormFromSpec, ackSpecApplies, ackSpecFromForm } from "./ackSpec";

// §83: форма ↔ спека. Ошибка здесь выглядит как «настройка не сохранилась» или
// «сохранилась не та» — то есть ровно как баг, ради которого раздел и написан.

const sigurTemplate = '{"confirmedLogId": "${ body.logs[*].logId | max }"}';

describe("ackSpecFromForm", () => {
  it("выключенный переключатель даёт null — «отвечать как раньше»", () => {
    const spec = ackSpecFromForm({
      ...ackFormDefaults,
      ack_enabled: false,
      // Черновик в поле остался, но сохраняться он не должен: иначе узел хранил
      // бы текст, который однажды «оживёт» при включении.
      ack_body: "черновик",
    }, "requestAsync");

    expect(spec).toBeNull();
  });

  it("включённый — собирает спеку первой версии", () => {
    const spec = ackSpecFromForm({
      ack_enabled: true,
      ack_content_type: "application/json",
      ack_status: 0,
      ack_on_error: "default",
      ack_body: sigurTemplate,
    }, "requestAsync");

    expect(spec).toEqual({
      version: 1,
      status: 0,
      content_type: "application/json",
      body: sigurTemplate,
      on_error: "default",
    });
  });

  it("сохраняет явный код и политику отказа", () => {
    const spec = ackSpecFromForm({
      ack_enabled: true,
      ack_content_type: "text/plain",
      ack_status: 202,
      ack_on_error: "error",
      ack_body: "${ query.code }",
    }, "requestAsync");

    expect(spec?.status).toBe(202);
    expect(spec?.content_type).toBe("text/plain");
    expect(spec?.on_error).toBe("error");
  });

  // Красное на прежнем коде: `ackSpecFromForm` тип узла не принимал вовсе и
  // собирала спеку одинаково, поэтому узел, сохранённый как sync, уносил на
  // сервер переопределение, которого в интерфейсе уже не видно.
  it.each(["request", "RabbitMQAsync"] as const)(
    "тип %s выключает переопределение, даже если поля заполнены",
    (rootMethod) => {
      const filled = {
        ack_enabled: true,
        ack_content_type: "application/json" as const,
        ack_status: 202,
        ack_on_error: "error" as const,
        ack_body: sigurTemplate,
      };

      expect(ackSpecFromForm(filled, rootMethod)).toBeNull();
    },
  );
});

// Одно правило на показ группы и на сборку payload'а: пока условие было
// записано дважды, форма прятала карточку у pull-узлов, но спеку бы отправила.
describe("ackSpecApplies (§83.5)", () => {
  it("переопределение существует только у requestAsync", () => {
    expect(ackSpecApplies("requestAsync")).toBe(true);
    expect(ackSpecApplies("request")).toBe(false);
    expect(ackSpecApplies("RabbitMQAsync")).toBe(false);
  });
});

describe("ackFormFromSpec", () => {
  it("узел без спеки — выключенный переключатель с дефолтами", () => {
    expect(ackFormFromSpec(null)).toEqual(ackFormDefaults);
    expect(ackFormFromSpec(undefined)).toEqual(ackFormDefaults);
  });

  it("раскладывает спеку по полям формы", () => {
    const fields = ackFormFromSpec({
      version: 1,
      status: 201,
      content_type: "text/plain",
      body: sigurTemplate,
      on_error: "error",
    });

    expect(fields).toEqual({
      ack_enabled: true,
      ack_content_type: "text/plain",
      ack_status: 201,
      ack_on_error: "error",
      ack_body: sigurTemplate,
    });
  });

  it("status=0 из спеки означает «как сейчас», а не отсутствие настройки", () => {
    const fields = ackFormFromSpec({
      version: 1,
      content_type: "application/json",
      body: sigurTemplate,
      on_error: "default",
    });

    expect(fields.ack_enabled).toBe(true);
    expect(fields.ack_status).toBe(0);
  });

  it("round-trip формы не меняет спеку", () => {
    const original = {
      version: 1,
      status: 200,
      content_type: "application/json" as const,
      body: sigurTemplate,
      on_error: "default" as const,
    };

    expect(ackSpecFromForm(ackFormFromSpec(original), "requestAsync")).toEqual(original);
  });
});
