-- Откат §40: вернуть CHECK-констрейнты без 'ANY'. ПРЕДУПРЕЖДЕНИЕ: упадёт, если
-- остались узлы с incoming_method/outgoing_method = 'ANY' — их надо привести к
-- конкретному методу до отката.

ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS nodes_incoming_method_check,
    ADD CONSTRAINT nodes_incoming_method_check
        CHECK (incoming_method IN ('GET', 'POST', 'PUT', 'DELETE')),
    DROP CONSTRAINT IF EXISTS nodes_outgoing_method_check,
    ADD CONSTRAINT nodes_outgoing_method_check
        CHECK (outgoing_method IN ('GET', 'POST', 'PUT', 'DELETE'));
