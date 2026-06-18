-- 0018_node_dlq_retry_delay.up.sql
--
-- §36 ТЗ: авто-репроцессор DLQ. Минимальная задержка перед повторной доставкой
-- ОШИБОЧНОЙ отправки: после неудачи (не-2xx) сообщение переотправляется в хвост
-- DLQ с next_attempt_at = now + dlq_retry_delay_seconds; репроцессор не пытается
-- доставить раньше этого момента (per-message backoff поверх интервала прохода).
-- NOT NULL DEFAULT 60 (60 секунд), диапазон 1с..24ч.
--
-- NOT NULL DEFAULT 60 атомарно ЗАПОЛНЯЕТ все существующие узлы значением 60 —
-- отдельный backfill-UPDATE не нужен.

ALTER TABLE nodes
    ADD COLUMN dlq_retry_delay_seconds INTEGER NOT NULL DEFAULT 60
        CHECK (dlq_retry_delay_seconds >= 1 AND dlq_retry_delay_seconds <= 86400);
