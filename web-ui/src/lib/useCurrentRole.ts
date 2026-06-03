import { useQuery } from "@tanstack/react-query";

import { api } from "../api/client";
import { roleAtLeast, type Role } from "./roles";

type MeResp = { user: { user_id: string; role: string } };

// useCurrentRole — текущая роль пользователя из /api/auth/me (кешируется).
export function useCurrentRole(): string | undefined {
  const me = useQuery({
    queryKey: ["me"],
    queryFn: () => api.get<MeResp>("/api/auth/me"),
    staleTime: 5 * 60_000,
  });
  return me.data?.user.role;
}

// useRoleAtLeast — true, если роль текущего пользователя не ниже min (§26).
// Пока роль не загружена — false (affordance скрыт до подтверждения прав).
export function useRoleAtLeast(min: Role): boolean {
  return roleAtLeast(useCurrentRole(), min);
}
