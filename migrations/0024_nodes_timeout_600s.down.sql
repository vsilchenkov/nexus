-- ВНИМАНИЕ: откат ужимает timeout_ms > 300000 до 300000 (потеря значения),
-- иначе восстановленный CHECK не пройдёт по существующим строкам.
UPDATE nodes SET timeout_ms = 300000 WHERE timeout_ms > 300000;
ALTER TABLE nodes DROP CONSTRAINT nodes_timeout_range;
ALTER TABLE nodes ADD CONSTRAINT nodes_timeout_range CHECK (timeout_ms BETWEEN 100 AND 300000);
