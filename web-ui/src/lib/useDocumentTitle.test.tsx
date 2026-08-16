import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useInstanceID } from "./instance";
import { APP_TITLE, documentTitle, useDocumentTitle } from "./useDocumentTitle";

// §89.2: заголовок вкладки браузера несёт код ноды («Nexus KZ»), чтобы при
// нескольких открытых нодах вкладки различались — в том числе на экране входа.

const { apiGet } = vi.hoisted(() => ({ apiGet: vi.fn() }));

vi.mock("../api/client", async () => {
  const actual = await vi.importActual<typeof import("../api/client")>("../api/client");
  return { ...actual, api: { ...actual.api, get: apiGet } };
});

function wrapper(qc: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

beforeEach(() => {
  apiGet.mockReset();
  document.title = APP_TITLE; // как в index.html до первого рендера
});

describe("documentTitle", () => {
  it("нода без идентификатора оставляет заголовок прежним", () => {
    expect(documentTitle("")).toBe("Nexus");
  });

  it("код ноды приписывается ЗАГЛАВНЫМИ", () => {
    expect(documentTitle("kz")).toBe("Nexus KZ");
  });

  it("пробелы по краям не попадают в заголовок", () => {
    expect(documentTitle(" kz ")).toBe("Nexus KZ");
  });
});

describe("useDocumentTitle", () => {
  it("ставит код ноды в заголовок вкладки", async () => {
    apiGet.mockResolvedValue({ version: "1.2.3", instance: "kz" });
    renderHook(() => useDocumentTitle(), { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(document.title).toBe("Nexus KZ"));
  });

  it("нода без идентификатора: заголовок не меняется", async () => {
    apiGet.mockResolvedValue({ version: "1.2.3" });
    renderHook(() => useDocumentTitle(), { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(document.title).toBe("Nexus");
  });

  // Заголовок вкладки — украшение, а не функция: недоступный /api/version не
  // должен ни ронять приложение, ни оставлять вкладку без имени.
  it("упавший запрос версии оставляет заголовок «Nexus»", async () => {
    apiGet.mockRejectedValue(new Error("boom"));
    renderHook(() => useDocumentTitle(), { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(apiGet).toHaveBeenCalled());
    expect(document.title).toBe("Nexus");
  });

  // Ключевое требование: хук ПЕРЕИСПОЛЬЗУЕТ запрос версии, который страница уже
  // делает ради футера сайдбара и формы входа. Отдельный вызов /api/version
  // ради заголовка вкладки был бы платой ни за что на каждой загрузке.
  it("не добавляет второй запрос /api/version", async () => {
    apiGet.mockResolvedValue({ version: "1.2.3", instance: "kz" });
    const qc = newClient();
    renderHook(
      () => {
        useDocumentTitle();
        return useInstanceID();
      },
      { wrapper: wrapper(qc) },
    );

    await waitFor(() => expect(document.title).toBe("Nexus KZ"));
    expect(apiGet).toHaveBeenCalledTimes(1);
    expect(apiGet).toHaveBeenCalledWith("/api/version");
  });
});
