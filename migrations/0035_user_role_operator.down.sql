-- 0035_user_role_operator.down.sql
--
-- Откат роли `operator` (§87). Сначала переводим всех операторов в `viewer`
-- (наименее привилегированная роль — безопасный дефолт, как в 0013), иначе
-- восстановление старого CHECK-constraint упадёт на существующих строках с
-- role='operator'.
--
-- ПОТЕРЯ ДАННЫХ: роль не восстанавливается повторным `up` — после отката и
-- наката операторы останутся наблюдателями, права придётся выдать заново.
-- Внимание при выпуске релиза: scripts/release/rollback_info.py помечает
-- потерю только по DROP TABLE / DROP COLUMN / DELETE FROM и этот UPDATE НЕ
-- увидит — строку «Откат» в CHANGELOG (§74.6) пишем руками.

UPDATE users SET role = 'viewer' WHERE role = 'operator';

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'manager', 'viewer'));
