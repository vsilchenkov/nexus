import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { AuditDetailsCell } from "./AuditDetailsCell";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

// §108: строка node.revert рендерится тем же diff, что node.update; служебные
// ключи отмены не выглядят полями узла; перебитое «Стало» зачёркнуто.
describe("AuditDetailsCell (§108)", () => {
  it("node.revert — diff без служебных ключей и строка источника", () => {
    render(
      <AuditDetailsCell
        action="node.revert"
        details={{
          status: { before: "enabled", after: "paused" },
          reverted_from: "e1",
          reverted_from_at: "2026-09-23T18:36:22Z",
        }}
      />,
    );
    expect(screen.getByText("audit.revert_source")).toBeInTheDocument();
    expect(screen.getByText("status")).toBeInTheDocument();
    expect(screen.queryByText("reverted_from")).toBeNull();
    expect(screen.queryByText("reverted_from_at")).toBeNull();
    expect(screen.queryByText("e1")).toBeNull();
  });

  it("перебитое поле зачёркнуто, текущее значение в подсказке", () => {
    const { container } = render(
      <AuditDetailsCell
        action="node.update"
        details={{
          status: { before: "paused", after: "enabled" },
          target_url: { before: "http://a", after: "http://b" },
        }}
        overwritten={{ status: "paused" }}
      />,
    );
    const struck = container.querySelectorAll("s");
    expect(struck).toHaveLength(1);
    expect(struck[0].textContent).toBe("enabled");
    expect(struck[0].parentElement?.getAttribute("title")).toBe("audit.diff.now");
    expect(screen.getByText("http://b").tagName).not.toBe("S");
  });

  it("без разметки — прежний вид", () => {
    const { container } = render(
      <AuditDetailsCell action="node.update" details={{ status: { before: "paused", after: "enabled" } }} />,
    );
    expect(container.querySelector("s")).toBeNull();
    expect(container.firstElementChild?.className).not.toContain("opacity-60");
  });
});
