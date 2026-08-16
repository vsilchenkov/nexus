// Общие помощники по паролям (§7.10, §88.8.3).
//
// Вынесены из settings/Users.tsx: те же функции нужны на публичной странице
// задания нового пароля, а третья копия появилась бы при следующем экране.

export const MIN_PASSWORD_LENGTH = 8;

export type PasswordStrength = { score: 0 | 1 | 2 | 3 | 4; key: string };

// passwordStrength — визуальный индикатор силы. Ничего не блокирует: сервер
// проверяет только минимальную длину (usecase.validatePassword), и расходиться
// с ним индикатор не должен.
export function passwordStrength(p: string): PasswordStrength {
  if (p.length === 0) return { score: 0, key: "settings.users.pw_empty" };
  let score = 0;
  if (p.length >= MIN_PASSWORD_LENGTH) score++;
  if (/[A-Z]/.test(p) && /[a-z]/.test(p)) score++;
  if (/\d/.test(p)) score++;
  if (/[^A-Za-z0-9]/.test(p)) score++;
  const key =
    score <= 1
      ? "settings.users.pw_weak"
      : score === 2
        ? "settings.users.pw_medium"
        : score === 3
          ? "settings.users.pw_strong"
          : "settings.users.pw_very_strong";
  return { score: score as 0 | 1 | 2 | 3 | 4, key };
}

// generatePassword — сильный пароль из 12 символов. Из алфавитов исключены
// визуально неотличимые знаки (0/O, 1/l/I): пароль часто переносят вручную.
export function generatePassword(): string {
  const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ";
  const lower = "abcdefghijkmnopqrstuvwxyz";
  const digits = "23456789";
  const symbols = "!@#$%^&*-_+=?";
  const all = upper + lower + digits + symbols;
  const pick = (set: string) => set[Math.floor(Math.random() * set.length)];
  const must = [pick(upper), pick(lower), pick(digits), pick(symbols)];
  const rest = Array.from({ length: 8 }, () => pick(all));
  return [...must, ...rest].sort(() => Math.random() - 0.5).join("");
}
