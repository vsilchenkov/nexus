import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DefaultPeriodButton } from "./DefaultPeriodButton";
import { PREF_KEY_NODE_PERIOD } from "../../lib/prefs";
import type { Period } from "../../lib/period";

// §92: кнопка «По умолчанию» — общая для рабочего стола, вкладок узла и монитора
// Kafka. Проверяем её собственный контракт: что она пишет, когда прячется и
// когда неактивна. Раскладка по экранам проверяется в тестах этих экранов.

const { apiGet, apiPut } = vi.hoisted(() => ({ apiGet: vi.fn(), apiPut: vi.fn() }));

vi.mock("../../api/client", async () => {
  const actual = await vi.importActual<typeof import("../../api/client")>("../../api/client");
  return { ...actual, api: { get: apiGet, put: apiPut } };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

const PRESET_24H: Period = { kind: "preset", range: "24h" };
const PRESET_7D: Period = { kind: "preset", range: "7d" };
const CUSTOM: Period = { kind: "custom", from: "2026-08-01T00:00:00Z", to: "2026-08-02T00:00:00Z" };

function renderButton(props: Parameters<typeof DefaultPeriodButton>[0]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  return render(<DefaultPeriodButton {...props} />, { wrapper });
}

describe("DefaultPeriodButton (§92)", () => {
  beforeEach(() => {
    apiGet.mockReset();
    apiPut.mockReset();
    apiGet.mockResolvedValue({ items: [] });
    apiPut.mockResolvedValue({});
  });

  it("сохраняет текущий пресет под переданными team_id и ключом", async () => {
    renderButton({
      period: PRESET_7D,
      savedDefault: PRESET_24H,
      teamId: "team-a",
      prefKey: PREF_KEY_NODE_PERIOD,
    });

    fireEvent.click(screen.getByRole("button"));

    await waitFor(() =>
      expect(apiPut).toHaveBeenCalledWith("/api/me/prefs", {
        team_id: "team-a",
        key: PREF_KEY_NODE_PERIOD,
        value: PRESET_7D,
      }),
    );
  });

  it("пустой teamId уходит как глобальный преф — это режим «Все команды» и монитор Kafka", async () => {
    renderButton({
      period: PRESET_7D,
      savedDefault: PRESET_24H,
      teamId: "",
      prefKey: PREF_KEY_NODE_PERIOD,
    });

    fireEvent.click(screen.getByRole("button"));

    await waitFor(() =>
      expect(apiPut).toHaveBeenCalledWith(
        "/api/me/prefs",
        expect.objectContaining({ team_id: "" }),
      ),
    );
  });

  it("неактивна, когда текущий период уже сохранён", () => {
    renderButton({
      period: PRESET_7D,
      savedDefault: PRESET_7D,
      teamId: "team-a",
      prefKey: PREF_KEY_NODE_PERIOD,
    });

    const btn = screen.getByRole("button");
    expect(btn).toBeDisabled();
    fireEvent.click(btn);
    expect(apiPut).not.toHaveBeenCalled();
  });

  it("не показывается для произвольного периода — календарный диапазон дефолтом не бывает", () => {
    renderButton({
      period: CUSTOM,
      savedDefault: PRESET_24H,
      teamId: "team-a",
      prefKey: PREF_KEY_NODE_PERIOD,
    });

    expect(screen.queryByRole("button")).toBeNull();
  });
});
