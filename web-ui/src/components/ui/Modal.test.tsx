import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { Modal } from "./Modal";
import { Popover, PopoverContent, PopoverTrigger } from "./Popover";

// §104.8: общий Modal — настоящий модальный диалог. До правки это был div без
// роли: экранный диктор не объявлял окно, фокус оставался на странице под
// подложкой, Tab уходил за окно.

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function Harness({ autoFocusInput = false }: { autoFocusInput?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open
      </button>
      {open && (
        <Modal
          title="Узлы группы"
          subtitle="Всего узлов: 4"
          onClose={() => setOpen(false)}
          footer={<button type="button">footer-last</button>}
        >
          <input aria-label="field" autoFocus={autoFocusInput} />
        </Modal>
      )}
    </>
  );
}

function PopoverHarness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open
      </button>
      {open && (
        <Modal title="Повтор" onClose={() => setOpen(false)}>
          <Popover>
            <PopoverTrigger asChild>
              <button type="button">menu</button>
            </PopoverTrigger>
            <PopoverContent>
              <div>menu-body</div>
            </PopoverContent>
          </Popover>
        </Modal>
      )}
    </>
  );
}

function openVia(): HTMLElement {
  const trigger = screen.getByRole("button", { name: "open" });
  trigger.focus();
  fireEvent.click(trigger);
  return trigger;
}

describe("Modal: семантика диалога (§104.8)", () => {
  it("роль dialog, aria-modal, имя — заголовок, описание — подзаголовок", () => {
    render(<Harness />);
    openVia();
    const dialog = screen.getByRole("dialog", { name: "Узлы группы" });
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(dialog).toHaveAccessibleDescription("Всего узлов: 4");
  });

  it("у крестика есть имя, крестик и Esc закрывают", () => {
    render(<Harness />);
    openVia();
    fireEvent.click(screen.getByRole("button", { name: "common.close" }));
    expect(screen.queryByRole("dialog")).toBeNull();

    openVia();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("при открытии фокус уходит в окно, при закрытии — обратно на источник", () => {
    render(<Harness />);
    const trigger = openVia();
    expect(screen.getByRole("dialog")).toHaveFocus();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(trigger).toHaveFocus();
  });

  it("поле с autoFocus фокус не теряет", () => {
    render(<Harness autoFocusInput />);
    openVia();
    expect(screen.getByRole("textbox", { name: "field" })).toHaveFocus();
  });

  it("Tab ходит по кругу внутри окна", () => {
    render(<Harness />);
    openVia();
    const dialog = screen.getByRole("dialog");
    const first = screen.getByRole("button", { name: "common.close" });
    const last = screen.getByRole("button", { name: "footer-last" });

    last.focus();
    fireEvent.keyDown(last, { key: "Tab" });
    expect(first).toHaveFocus();

    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();

    // С самого окна (фокус после открытия) Shift+Tab тоже не уходит наружу.
    dialog.focus();
    fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
    expect(last).toHaveFocus();
  });

  // Стенд §104.8: в «Повторе за период» Esc, закрывавший выпадающий список
  // «Клиент», закрывал и всё окно — выбранные период и фильтры терялись.
  // Radix-слой, закрываясь по Esc, помечает событие preventDefault.
  it("Esc, закрывший выпадающий список, окно не закрывает", async () => {
    render(<PopoverHarness />);
    openVia();
    fireEvent.click(screen.getByRole("button", { name: "menu" }));
    expect(await screen.findByText("menu-body")).toBeInTheDocument();

    fireEvent.keyDown(screen.getByText("menu-body"), { key: "Escape" });
    await waitFor(() => expect(screen.queryByText("menu-body")).toBeNull());
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("в середине окна Tab не перехватывается", () => {
    render(<Harness />);
    openVia();
    const field = screen.getByRole("textbox", { name: "field" });
    field.focus();
    const ev = fireEvent.keyDown(field, { key: "Tab" });
    // fireEvent возвращает false, если обработчик вызвал preventDefault.
    expect(ev).toBe(true);
  });
});
