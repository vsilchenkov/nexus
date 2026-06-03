-- 0015_node_methods.up.sql
--
-- §3.2 ТЗ (#5): отдельные HTTP-методы узла для входящего и исходящего запроса.
--   incoming_method — метод, который узел принимает на вход (иначе 405).
--   outgoing_method — метод, которым Sender вызывает получателя.
-- Оба по умолчанию POST — это сохраняет текущее поведение для существующих узлов
-- (раньше исходящий метод повторял метод входящего запроса; POST — самый частый).

ALTER TABLE nodes
    ADD COLUMN incoming_method TEXT NOT NULL DEFAULT 'POST'
        CHECK (incoming_method IN ('GET', 'POST', 'PUT', 'DELETE')),
    ADD COLUMN outgoing_method TEXT NOT NULL DEFAULT 'POST'
        CHECK (outgoing_method IN ('GET', 'POST', 'PUT', 'DELETE'));
