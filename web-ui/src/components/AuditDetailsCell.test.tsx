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

  // Раскладку jsdom не считает — тест держит причину бага: в таблице
  // «поле | Было | Стало» с break-all колонке значения доставалось два-три
  // символа, и длинный async_ack_spec шёл по буквам. Длинная пара теперь —
  // строками «Было»/«Стало» под полем, короткая — одной строкой со стрелкой.
  it("длинная пара — строками под полем, короткая — в строку", () => {
    const long = 'status=0 content_type=application/json body={"confirmedLogId":"${body.logs[*].logId | max}"}';
    const { container } = render(
      <AuditDetailsCell
        action="node.update"
        details={{
          async_ack_spec: { before: long, after: "" },
          status: { before: "paused", after: "enabled" },
          auth_credentials: "changed",
        }}
      />,
    );
    expect(container.querySelector("table")).toBeNull();
    expect(container.querySelector(".break-all")).toBeNull();

    // Подписи — только у длинной пары, по одной.
    expect(screen.getAllByText("audit.diff.before")).toHaveLength(1);
    expect(screen.getAllByText("audit.diff.after")).toHaveLength(1);
    const longValue = screen.getByText(long);
    expect(longValue.className).toContain("[overflow-wrap:anywhere]");
    expect(longValue.previousElementSibling?.textContent).toBe("audit.diff.before");
    expect(screen.getByText("(empty)").previousElementSibling?.textContent).toBe("audit.diff.after");

    // Короткая пара: «paused → enabled» рядом с именем поля.
    const inline = screen.getByText("paused").parentElement;
    expect(inline?.textContent).toBe("paused → enabled");
    expect(inline?.previousElementSibling?.textContent).toBe("status");
    expect(screen.getByText("changed").previousElementSibling?.textContent).toBe("auth_credentials");
  });

  it("без разметки — прежний вид", () => {
    const { container } = render(
      <AuditDetailsCell action="node.update" details={{ status: { before: "paused", after: "enabled" } }} />,
    );
    expect(container.querySelector("s")).toBeNull();
    expect(container.firstElementChild?.className).not.toContain("opacity-60");
  });
});
