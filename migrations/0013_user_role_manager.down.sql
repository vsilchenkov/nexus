-- 0013_user_role_manager.down.sql
--
-- Откат роли `manager` (§26). Сначала переводим всех менеджеров в `viewer`
-- (наименее привилегированная роль — безопасный дефолт), иначе восстановление
-- старого CHECK-constraint упадёт на существующих строках с role='manager'.

UPDATE users SET role = 'viewer' WHERE role = 'manager';

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'viewer'));
