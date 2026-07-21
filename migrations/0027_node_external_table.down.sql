-- 0027_node_external_table.down.sql
--
-- Проставленный up-миграцией дефолтный clickhouse_template_id НЕ откатывается:
-- определить, у каких узлов он был NULL до миграции, уже нельзя, а шаблон сам
-- по себе безвреден (он лишь фиксирует, что таблицей управляет Nexus, — то же
-- поведение, что было у этих узлов до §64).
ALTER TABLE nodes DROP COLUMN IF EXISTS external_table;
