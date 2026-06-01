-- 0014_rmq_async_node.up.sql
--
-- §27 ТЗ: третий тип узла RabbitMQAsync — узел сам забирает сообщения из очереди
-- RabbitMQ (Puller-воркер в Receiver) и публикует их в Kafka nexus.async, дальше
-- Sender обрабатывает их как обычный requestAsync.
--
-- 1) Расширяем список методов (methods — справочник, FK из nodes.root_method).
-- 2) Добавляем nullable-колонки rmq_*/pull_* в nodes (для request/requestAsync — NULL).
-- 3) chk_rmq_fields защищает от полу-настроенного RabbitMQ-узла на уровне БД
--    (дублирует доменную Node.Validate()).

ALTER TABLE methods DROP CONSTRAINT methods_name_check;
ALTER TABLE methods ADD CONSTRAINT methods_name_check
    CHECK (name IN ('request', 'requestAsync', 'RabbitMQAsync'));
INSERT INTO methods (name) VALUES ('RabbitMQAsync') ON CONFLICT DO NOTHING;

ALTER TABLE nodes
    ADD COLUMN rmq_host          VARCHAR(253),
    ADD COLUMN rmq_port          INTEGER,
    ADD COLUMN rmq_vhost         VARCHAR(255),
    ADD COLUMN rmq_user          VARCHAR(255),
    ADD COLUMN rmq_password      TEXT,
    ADD COLUMN rmq_queue         VARCHAR(255),
    ADD COLUMN rmq_use_tls       BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN pull_interval_sec INTEGER,
    ADD COLUMN pull_batch_size   INTEGER,
    ADD COLUMN pull_prefetch     INTEGER;

ALTER TABLE nodes ADD CONSTRAINT chk_rmq_fields CHECK (
    root_method <> 'RabbitMQAsync' OR (
        rmq_host  IS NOT NULL AND char_length(rmq_host)  > 0 AND
        rmq_queue IS NOT NULL AND char_length(rmq_queue) > 0 AND
        pull_interval_sec BETWEEN 1 AND 3600 AND
        pull_batch_size   BETWEEN 1 AND 1000
    )
);
