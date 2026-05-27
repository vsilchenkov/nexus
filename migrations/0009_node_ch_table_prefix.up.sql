-- 0009_node_ch_table_prefix.up.sql
--
-- Phase 10.C.3 (multi-tenancy v2): backfill nodes.clickhouse_table.
--
-- До Phase 10 Sender писал в `cfg.ClickHouse.Database` (vika_logs) +
-- node.clickhouse_table — формат поля был либо просто `<table>`, либо
-- `vika_logs.<table>` (loadtest). После Phase 10.C.2 NodeUsecase
-- нормализует поле до `<team.ch_database>.<table>` при Create/Update,
-- а Sender пишет напрямую в это полное имя.
--
-- Эта миграция приводит существующие записи к новому формату для
-- default-team:
--   1. `<table>` (без точки)        → `nexus_default.<table>`
--   2. `vika_logs.<table>`          → `nexus_default.<table>`
--   3. `nexus_default.<table>`      → без изменений (идемпотентность)
--   4. `nexus_<other>.<table>`      → без изменений (multi-team данные)
--
-- ВАЖНО: миграция переименовывает только PG-метаданные. Физический
-- перенос данных в ClickHouse (RENAME TABLE vika_logs.X TO
-- nexus_default.X или re-create + insert ... select) — отдельный
-- эксплуатационный шаг, который должен выполнить администратор перед
-- разворотом этой миграции на проде. На greenfield-стенде (см. C.1
-- AskUserQuestion) данных нет — миграция фактически no-op для CH.

UPDATE nodes
   SET clickhouse_table = 'nexus_default.' || clickhouse_table
 WHERE clickhouse_table <> ''
   AND position('.' in clickhouse_table) = 0;

UPDATE nodes
   SET clickhouse_table = 'nexus_default.' || substring(clickhouse_table from char_length('vika_logs.') + 1)
 WHERE clickhouse_table LIKE 'vika_logs.%';
