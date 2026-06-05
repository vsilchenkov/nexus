import { createContext, useContext } from "react";

// ConfirmOptions — параметры модального подтверждения (QA-2026-02 / П16).
export type ConfirmOptions = {
  title: string;
  message?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  // danger — красная кнопка подтверждения для деструктивных действий (удаление).
  danger?: boolean;
};

// ConfirmFn — императивный вызов: возвращает Promise<true|false>.
export type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

// ConfirmContext — провайдер монтируется один раз в App (см. ConfirmProvider).
export const ConfirmContext = createContext<ConfirmFn | null>(null);

// useConfirm — заменяет нативный window.confirm на единый модальный диалог
// дизайн-системы. Использование: `if (await confirm({ title, message })) …`.
export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext);
  if (!ctx) throw new Error("useConfirm must be used within <ConfirmProvider>");
  return ctx;
}
