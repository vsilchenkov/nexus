import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TooltipProvider } from "@radix-ui/react-tooltip";
import { beforeEach, describe, expect, it, vi } from "vitest";

// §84.7: исход последнего вызова узла в шапке страницы. Закрывает follow-up
// §52: до раздела трёхсостоянье было только на рабочем столе, а на странице
// узла стоял лишь конфигурационный статус — «Активен» показывался и у узла, к
// которому доставка не проходит вовсе.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "ru" } }),
}));

import { NodeRuntimeBadge } from "./NodeRuntimeBadge";

function renderBadge() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <NodeRuntimeBadge nodeId="n1" />
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => apiGet.mockReset());

describe("NodeRuntimeBadge (§84.7)", () => {
  it("down → бейдж ошибочного тона", async () => {
    apiGet.mockResolvedValue({ available: true, last_outcome: "down", source: "redis" });
    renderBadge();
    await waitFor(() => expect(screen.getByText("node.runtime.down")).toBeInTheDocument());
  });

  it("degraded → бейдж предупреждающего тона", async () => {
    apiGet.mockResolvedValue({ available: true, last_outcome: "degraded", source: "prometheus" });
    renderBadge();
    await waitFor(() => expect(screen.getByText("node.runtime.degraded")).toBeInTheDocument());
  });

  // Зелёная плашка на каждом узле — шум, который перестают замечать раньше,
  // чем он понадобится.
  it("ok → бейджа нет вовсе", async () => {
    apiGet.mockResolvedValue({ available: true, last_outcome: "ok", source: "redis" });
    const { container } = renderBadge();
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });

  // Ключевое правило: «неизвестно» НЕ красится в ok. Соврать оператору «всё в
  // порядке» хуже, чем не сказать ничего.
  it("available=false → бейджа нет, и это НЕ ok", async () => {
    apiGet.mockResolvedValue({ available: false, last_outcome: "ok", source: "none" });
    const { container } = renderBadge();
    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(container.textContent).toBe("");
    expect(screen.queryByText("node.runtime.down")).not.toBeInTheDocument();
  });

  // Пока ответа нет — шапка не должна мигать бейджем. Проверяется синхронно,
  // до разрешения запроса: висящий промис подвешивает и весь прогон vitest.
  //
  // Отдельного кейса «запрос упал» здесь нет намеренно: для компонента это ТО
  // ЖЕ состояние (данных нет → null), а искусственно отклонённый промис валит
  // сам тест по политике vitest об необработанных отклонениях, ничего при этом
  // не проверяя.
  it("до ответа сервера бейджа нет", () => {
    apiGet.mockResolvedValue({ available: true, last_outcome: "down", source: "redis" });
    const { container } = renderBadge();
    expect(container.textContent).toBe("");
  });

  it("запрос идёт на эндпоинт runtime этого узла", async () => {
    apiGet.mockResolvedValue({ available: true, last_outcome: "down", source: "redis" });
    renderBadge();
    await waitFor(() => expect(apiGet).toHaveBeenCalledWith("/api/nodes/n1/runtime"));
  });
});
