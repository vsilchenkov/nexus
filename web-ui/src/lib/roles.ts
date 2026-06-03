// Роли доступа UI (§26). Иерархия прав: viewer < manager < admin.
// Зеркалит domain.UserRole.Rank() на бэкенде.
export type Role = "admin" | "manager" | "viewer";

const RANK: Record<Role, number> = { viewer: 0, manager: 1, admin: 2 };

// roleRank — числовой ранг роли; неизвестная роль трактуется как минимум.
export function roleRank(role: string | undefined): number {
  return RANK[role as Role] ?? 0;
}

// roleAtLeast — true, если роль не ниже min по иерархии прав.
export function roleAtLeast(role: string | undefined, min: Role): boolean {
  return roleRank(role) >= RANK[min];
}
