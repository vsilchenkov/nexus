import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { AckHelpDialog } from "./AckHelpDialog";

// §83: справка — не декорация. Проверяем два обещания раздела: примеры в ней
// кликабельные (оператор не набирает синтаксис руками) и подставляются в поле
// ровно теми шаблонами, которые описаны в ТЗ.

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

describe("AckHelpDialog", () => {
  it("показывает разделы справки и рабочие примеры", () => {
    render(<AckHelpDialog onClose={vi.fn()} onUse={vi.fn()} />);

    // Ключевые разделы на месте: что применяется, ограничения, причины отказа.
    expect(screen.getByText("node.ack.help.applies_title")).toBeInTheDocument();
    expect(screen.getByText("node.ack.help.limits_title")).toBeInTheDocument();
    expect(screen.getByText("node.ack.help.reasons_title")).toBeInTheDocument();

    // Пример из боевого кейса Sigur присутствует дословно.
    expect(
      screen.getByText('{"confirmedLogId": "${ body.logs[*].logId | max }"}'),
    ).toBeInTheDocument();
  });

  it("кнопка «Использовать» отдаёт шаблон и закрывает справку", () => {
    const onUse = vi.fn();
    const onClose = vi.fn();
    render(<AckHelpDialog onClose={onClose} onUse={onUse} />);

    fireEvent.click(screen.getAllByText("node.ack.help.use")[0]);

    expect(onUse).toHaveBeenCalledWith('{"confirmedLogId": "${ body.logs[*].logId | max }"}');
    // Закрытие делает вызывающая сторона внутри onUse — сам диалог onClose
    // на этом пути не дёргает, иначе форма закрылась бы дважды.
    expect(onClose).not.toHaveBeenCalled();
  });

  it("предупреждает, что заголовки запроса источником не являются", () => {
    render(<AckHelpDialog onClose={vi.fn()} onUse={vi.fn()} />);

    // Граница безопасности §83 должна быть видна оператору, а не только в коде.
    expect(screen.getByText("node.ack.help.sources_no_headers")).toBeInTheDocument();
  });

  it("предупреждает, что ответ подтверждает приём, а не доставку", () => {
    render(<AckHelpDialog onClose={vi.fn()} onUse={vi.fn()} />);

    expect(screen.getByText("node.ack.help.meaning_text")).toBeInTheDocument();
  });
});
