-- 0015_node_methods.down.sql
ALTER TABLE nodes
    DROP COLUMN incoming_method,
    DROP COLUMN outgoing_method;
