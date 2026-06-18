-- 0017_node_dlq_ttl.up.sql
--
-- §36 ТЗ: авто-репроцессор DLQ. Каждый узел держит свой TTL для повторной
-- доставки неудачных async-сообщений из nexus.async.dlq. По истечении
-- received_at + dlq_ttl_seconds — терминальный отказ (репроцессор сдаётся).
-- NOT NULL DEFAULT 86400 (24 часа), диапазон 60с..30сут.

ALTER TABLE nodes
    ADD COLUMN dlq_ttl_seconds INTEGER NOT NULL DEFAULT 86400
        CHECK (dlq_ttl_seconds >= 60 AND dlq_ttl_seconds <= 2592000);
