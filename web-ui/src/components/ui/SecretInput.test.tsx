import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { SecretInput } from "./SecretInput";

// §98.7: поле секрета чужой системы не должно опознаваться менеджером паролей.
// Менеджер Chrome/Edge ищет поле по type="password", поэтому маскировка делается
// CSS'ом на обычном текстовом поле — а там, где -webkit-text-security не
// поддержан, поле обязано вернуться к type="password", но никак не показать
// секрет открытым текстом.

// jsdom не реализует CSS.supports вовсе, поэтому обе ветки задаются явно.
function withMaskSupport(supported: boolean) {
  const css = (globalThis as { CSS?: unknown }).CSS;
  Object.defineProperty(globalThis, "CSS", {
    configurable: true,
    value: { ...(typeof css === "object" ? css : {}), supports: () => supported },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  Object.defineProperty(globalThis, "CSS", { configurable: true, value: undefined });
});

describe("SecretInput — защита от менеджера паролей (§98.7)", () => {
  it("секрет чужой системы: не поле пароля, маскируется CSS, помечен для менеджеров", () => {
    withMaskSupport(true);
    render(<SecretInput value="s3cret" onChange={() => {}} aria-label="secret" />);
    const el = screen.getByLabelText("secret");

    expect(el).toHaveAttribute("type", "text");
    expect(el.className).toContain("secret-mask");
    expect(el).toHaveAttribute("autocomplete", "off");
    expect(el).toHaveAttribute("name", "secret");
    expect(el).toHaveAttribute("data-1p-ignore");
    expect(el).toHaveAttribute("data-lpignore", "true");
    expect(el).toHaveAttribute("data-form-type", "other");
  });

  it("без поддержки -webkit-text-security откатывается к type=password, а не к открытому тексту", () => {
    withMaskSupport(false);
    render(<SecretInput value="s3cret" onChange={() => {}} aria-label="secret" />);
    const el = screen.getByLabelText("secret");

    expect(el).toHaveAttribute("type", "password");
    expect(el.className).not.toContain("secret-mask");
    // Пометки для сторонних менеджеров остаются и в откатной ветке.
    expect(el).toHaveAttribute("data-lpignore", "true");
  });

  it("глазик раскрывает значение: маскировки нет ни CSS-ом, ни типом", () => {
    withMaskSupport(true);
    render(<SecretInput value="s3cret" onChange={() => {}} aria-label="secret" />);
    fireEvent.click(screen.getByLabelText("show secret"));

    const el = screen.getByLabelText("secret");
    expect(el).toHaveAttribute("type", "text");
    expect(el.className).not.toContain("secret-mask");
  });

  it("собственный пароль пользователя: поведение прежнее, менеджер не отключается", () => {
    withMaskSupport(true);
    render(<SecretInput ownPassword value="s3cret" onChange={() => {}} aria-label="secret" />);
    const el = screen.getByLabelText("secret");

    expect(el).toHaveAttribute("type", "password");
    expect(el.className).not.toContain("secret-mask");
    expect(el).toHaveAttribute("autocomplete", "new-password");
    expect(el).not.toHaveAttribute("data-1p-ignore");
    expect(el).not.toHaveAttribute("name");
  });
});
