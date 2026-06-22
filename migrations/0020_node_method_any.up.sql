-- 0020_node_method_any.up.sql
--
-- §40 ТЗ: значение «Любой» (ANY) для входящего и исходящего HTTP-метода узла.
--   incoming_method=ANY — узел принимает запрос с любым методом (нет 405).
--   outgoing_method=ANY — Sender вызывает получателя тем же методом, что пришёл.
-- Пересоздаём CHECK-констрейнты (добавленные миграцией 0015) с включением 'ANY'.
-- Аддитивно: существующие значения GET/POST/PUT/DELETE остаются валидны.

ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS nodes_incoming_method_check,
    ADD CONSTRAINT nodes_incoming_method_check
        CHECK (incoming_method IN ('GET', 'POST', 'PUT', 'DELETE', 'ANY')),
    DROP CONSTRAINT IF EXISTS nodes_outgoing_method_check,
    ADD CONSTRAINT nodes_outgoing_method_check
        CHECK (outgoing_method IN ('GET', 'POST', 'PUT', 'DELETE', 'ANY'));
