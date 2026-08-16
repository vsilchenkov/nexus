-- 0035_user_role_operator.up.sql
--
-- §87 ТЗ: четвёртая UI-роль `operator` между `viewer` и `manager`. Оператор
-- эксплуатирует узел — вкладка «Очередь» целиком, пауза/отключение/включение,
-- сброс защиты, повтор запросов из логов, чтение Audit log, — но узлы не
-- создаёт, не меняет и не копирует; каталоги, dry-run и схемы CH остаются за
-- `manager`.
--
-- Меняем только CHECK-constraint на users.role — само значение роли хранится
-- в существующей колонке role VARCHAR(32) (см. 0002_nodes_methods_users,
-- расширенной в 0013_user_role_manager).

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'manager', 'operator', 'viewer'));
