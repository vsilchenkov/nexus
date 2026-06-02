-- 0016_node_comment.up.sql
-- §29: произвольный комментарий-описание узла (UI-метаданные).
-- Необязательное, максимум 2000 символов. NOT NULL DEFAULT '' — проще в Go
-- (без указателей), как incoming_method/webhook_signature_header.
ALTER TABLE nodes
    ADD COLUMN comment TEXT NOT NULL DEFAULT ''
        CHECK (length(comment) <= 2000);
