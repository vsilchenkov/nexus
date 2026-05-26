-- 0007_webhook_signature.down.sql
--
-- Откат webhook_signature — удаляем колонки и возвращаем старый CHECK.
-- ВНИМАНИЕ: если в БД остались узлы с incoming_auth_type='webhook_signature',
-- DROP CONSTRAINT упадёт — это by design (нельзя терять данные молча).

ALTER TABLE nodes DROP CONSTRAINT nodes_webhook_sig_header_required;
ALTER TABLE nodes DROP CONSTRAINT nodes_inc_auth_check;
ALTER TABLE nodes ADD  CONSTRAINT nodes_inc_auth_check
    CHECK (incoming_auth_type IN ('none', 'basic', 'token'));

ALTER TABLE nodes DROP COLUMN webhook_signature_prefix;
ALTER TABLE nodes DROP COLUMN webhook_signature_header;
