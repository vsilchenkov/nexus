-- 0009_node_ch_table_prefix.down.sql
--
-- Откат Phase 10.C.3: возвращаем legacy-формат `vika_logs.<table>` для
-- узлов default-team. Узлы других команд (nexus_<other>.<x>) трогать
-- нельзя — их разворот предполагает откат всего блока C.

UPDATE nodes
   SET clickhouse_table = 'vika_logs.' || substring(clickhouse_table from char_length('nexus_default.') + 1)
 WHERE clickhouse_table LIKE 'nexus_default.%';
