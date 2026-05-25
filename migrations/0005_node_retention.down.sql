-- 0005_node_retention.down.sql
ALTER TABLE nodes DROP COLUMN clickhouse_retention_days;
