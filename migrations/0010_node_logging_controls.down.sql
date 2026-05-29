-- 0010_node_logging_controls.down.sql
ALTER TABLE nodes
    DROP COLUMN logging_enabled,
    DROP COLUMN max_body_size_enabled,
    DROP COLUMN max_body_size;
