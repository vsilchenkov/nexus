-- §66: отображаемое имя пользователя. Обязательно при создании/изменении
-- (валидация на API), в интерфейсе везде выводится имя вместо логина.
-- Backfill: существующим пользователям имя = логину (администратор поправит
-- вручную) — поэтому исторические снапшоты логина (nodes.created_by/updated_by,
-- user_audit.user_login) на момент перехода совпадают с именем.
ALTER TABLE users ADD COLUMN IF NOT EXISTS name VARCHAR(255) NOT NULL DEFAULT '';

UPDATE users SET name = login WHERE name = '';
