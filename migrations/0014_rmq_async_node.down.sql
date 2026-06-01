-- 0014_rmq_async_node.down.sql
--
-- Откат §27. Сначала удаляем RabbitMQAsync-узлы (иначе восстановление старого
-- methods CHECK и удаление колонок rmq_* нарушат целостность), затем колонки и
-- значение метода.

DELETE FROM nodes WHERE root_method = 'RabbitMQAsync';

ALTER TABLE nodes DROP CONSTRAINT chk_rmq_fields;

ALTER TABLE nodes
    DROP COLUMN rmq_host,
    DROP COLUMN rmq_port,
    DROP COLUMN rmq_vhost,
    DROP COLUMN rmq_user,
    DROP COLUMN rmq_password,
    DROP COLUMN rmq_queue,
    DROP COLUMN rmq_use_tls,
    DROP COLUMN pull_interval_sec,
    DROP COLUMN pull_batch_size,
    DROP COLUMN pull_prefetch;

DELETE FROM methods WHERE name = 'RabbitMQAsync';
ALTER TABLE methods DROP CONSTRAINT methods_name_check;
ALTER TABLE methods ADD CONSTRAINT methods_name_check
    CHECK (name IN ('request', 'requestAsync'));
