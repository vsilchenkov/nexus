-- 0040_rejected_requests.down.sql
--
-- Откат §94.
--
-- ПОТЕРЯ ДАННЫХ: удаляется весь журнал отказов на входе. Практического вреда
-- нет — это наблюдаемость, а не учёт: после возврата на прежнюю версию кода
-- журнал всё равно никто не пишет и не читает, а после повторного наката он
-- наполнится заново за срок хранения. rollback_info.py пометит потерю сам
-- (DROP TABLE).
--
-- Порядок обратный созданию: дочерние таблицы держат FK на rejected_groups.

DROP TABLE IF EXISTS rejected_samples;
DROP TABLE IF EXISTS rejected_clients;
DROP TABLE IF EXISTS rejected_groups;
