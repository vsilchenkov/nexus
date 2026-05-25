-- 0005_node_retention.up.sql
--
-- §4.3 ТЗ: ClickHouse partition-drop housekeeping. Каждый узел держит свой
-- срок хранения логов; housekeeping-cron в Sender дропает старые партиции.
-- Поле NOT NULL DEFAULT 90 (~3 месяца) — рекомендованное по умолчанию.

ALTER TABLE nodes
    ADD COLUMN clickhouse_retention_days INTEGER NOT NULL DEFAULT 90
        CHECK (clickhouse_retention_days >= 0 AND clickhouse_retention_days <= 3650);
