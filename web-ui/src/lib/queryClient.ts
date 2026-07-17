import { QueryClient } from "@tanstack/react-query";

// Единственный QueryClient приложения. Вынесен из main.tsx, чтобы
// не-React-код (axios-interceptor в api/client.ts) мог сбросить кеш при
// 401/logout — иначе после повторного логина другим пользователем/командой
// кратко показываются чужие данные из кеша (Phase AUD.6).
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (failureCount, error: unknown) => {
        // 4xx не ретраим: это ответ «так не бывает», а не сбой связи —
        // повтор даст тот же результат. 401/403 разбирает глобальный
        // interceptor (редирект на login). 404 особенно важен: узел чужой
        // команды после переключения отдаёт 404, и три попытки с бэкоффом на
        // каждый refetchInterval держали useIsFetching() > 0 практически
        // постоянно — кнопка «Обновить» висела заблокированной бесконечно.
        // Ретраим только 5xx/сетевые (в т.ч. error без response).
        const status = (error as { response?: { status?: number } })?.response?.status;
        if (status !== undefined && status >= 400 && status < 500) return false;
        return failureCount < 2;
      },
      staleTime: 30_000,
    },
  },
});
