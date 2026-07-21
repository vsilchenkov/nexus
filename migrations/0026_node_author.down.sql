-- 0026_node_author.down.sql
ALTER TABLE nodes DROP COLUMN IF EXISTS updated_by;
ALTER TABLE nodes DROP COLUMN IF EXISTS created_by;
