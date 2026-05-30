-- 0013_user_role_manager.up.sql
--
-- §26 ТЗ: третья UI-роль `manager` между `viewer` и `admin`. Менеджер
-- управляет узлами и каталогами Allowed Hosts/Headers, видит Audit log и
-- меняет только свой пароль; общие настройки, пользователи, команды и
-- шаблоны CH остаются за `admin`.
--
-- Меняем только CHECK-constraint на users.role — само значение роли хранится
-- в существующей колонке role VARCHAR(32) (см. 0002_nodes_methods_users).

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'manager', 'viewer'));
