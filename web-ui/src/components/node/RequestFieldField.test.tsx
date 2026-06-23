import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { RequestFieldField } from "./RequestFieldField";

// Мокаем модуль api (не axios) — комбобокс не должен ходить в сеть в тестах.
vi.mock("../../api/client", () => ({
  api: { get: vi.fn().mockResolvedValue({ items: [] }), post: vi.fn() },
}));

function renderField(props: { value: string; onChange: (v: string) => void; invalid?: boolean }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <RequestFieldField {...props} />
    </QueryClientProvider>,
  );
}

describe("RequestFieldField", () => {
  it("показывает placeholder при пустом значении", () => {
    renderField({ value: "", onChange: () => {} });
    // Без i18n-провайдера t() возвращает сам ключ — этого достаточно для проверки.
    expect(screen.getByText("node.request_field_combo.placeholder")).toBeTruthy();
  });

  it("показывает выбранное значение", () => {
    renderField({ value: "apikey", onChange: () => {} });
    expect(screen.getByText("apikey")).toBeTruthy();
  });

  it("красит рамку при invalid (обязательное пустое поле)", () => {
    const { container } = renderField({ value: "", onChange: () => {}, invalid: true });
    expect(container.querySelector(".border-err")).toBeTruthy();
  });
});
