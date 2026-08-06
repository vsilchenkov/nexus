-- 0034_node_async_ack_spec.down.sql
--
-- Откат теряет настроенные шаблоны ответа приёма (§83): узлы возвращаются к
-- ответу {"result":true,"id":…}, и клиент с курсором снова перестаёт двигать
-- свой указатель. Данных доставки это не касается.
--
-- Порядок в аварии: сначала вернуть старый образ Receiver'а, потом накатывать
-- этот down. Обратный порядок оставит работающий новый Receiver без колонки, и
-- КАЖДЫЙ резолв узла упадёт на «column async_ack_spec does not exist» —
-- то есть встанет весь приём, а не одна фича (см. DEPLOYMENT §10).
--
-- Чаще всего откатывать схему не нужно вовсе: колонка аддитивна и старому коду
-- не мешает (§74.2).
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_async_ack_spec_size;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_async_ack_spec_object;
ALTER TABLE nodes DROP COLUMN IF EXISTS async_ack_spec;
