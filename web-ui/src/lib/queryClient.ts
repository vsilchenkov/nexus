import { QueryClient } from "@tanstack/react-query";

// Единственный QueryClient приложения. Вынесен из main.tsx, чтобы
// не-React-код (axios-interceptor в api/client.ts) мог сбросить кеш при
// 401/logout — иначе после повторного логина другим пользователем/командой
// кратко показываются чужие данные из кеша (Phase AUD.6).
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error: unknown) => {
        // Не retry-им 401/403 — пусть глобальный interceptor редиректит на login.
        const status = (error as { response?: { status?: number } })?.response?.status;
        if (status === 401 || status === 403) return false;
        return failureCount < 2;
      },
      staleTime: 30_000,
    },
  },
});
