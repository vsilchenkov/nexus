// Роли доступа UI (§26, §87). Иерархия прав: viewer < operator < manager < admin.
// Зеркалит domain.UserRole.Rank() на бэкенде.
//
// Ранг вычисляемый: сервер отдаёт СТРОКУ роли (/api/auth/me), а не её номер, —
// поэтому вставка `operator` в середину иерархии не потребовала ни миграции,
// ни разлогина. Менять числа здесь безопасно ровно до тех пор, пока это так.
export type Role = "admin" | "manager" | "operator" | "viewer";

const RANK: Record<Role, number> = { viewer: 0, operator: 1, manager: 2, admin: 3 };

// roleRank — числовой ранг роли; неизвестная роль трактуется как минимум.
export function roleRank(role: string | undefined): number {
  return RANK[role as Role] ?? 0;
}

// roleAtLeast — true, если роль не ниже min по иерархии прав.
export function roleAtLeast(role: string | undefined, min: Role): boolean {
  return roleRank(role) >= RANK[min];
}
