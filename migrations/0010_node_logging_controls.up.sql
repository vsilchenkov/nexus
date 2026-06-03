-- 0010_node_logging_controls.up.sql
--
-- §22 ТЗ: контроль логирования на уровне узла.
--   logging_enabled       — мастер-тумблер: при false узел не пишет лог в ClickHouse вообще.
--   max_body_size_enabled — включает ограничение размера сохраняемых тел request/response.
--   max_body_size         — максимальное число СИМВОЛОВ (рун) в сохраняемом теле; при включённом
--                           тумблере поля request/response режутся до этого значения.
-- Дефолты сохраняют текущее поведение: логирование включено, лимита нет.

ALTER TABLE nodes
    ADD COLUMN logging_enabled       BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN max_body_size_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN max_body_size         INTEGER NOT NULL DEFAULT 0
        CHECK (max_body_size >= 0 AND max_body_size <= 10000000);
