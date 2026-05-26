-- 0007_webhook_signature.up.sql
--
-- §16 ТЗ: Webhook signature verification.
-- Добавляет ещё один режим incoming_auth — 'webhook_signature' — для приёма
-- входящих webhook'ов с HMAC-SHA256 подписью в HTTP-заголовке (типовая схема
-- Stripe / GitHub / GitLab / Slack: подпись в заголовке вида
-- "X-Hub-Signature-256: sha256=<hex>").
--
-- Webhook secret хранится в существующей колонке incoming_auth_credentials
-- (AES-256-GCM, как для basic/token) — не вводим новую зашифрованную колонку.
-- Дополнительно нужны два технических параметра:
--   webhook_signature_header — имя HTTP-заголовка с подписью;
--   webhook_signature_prefix — префикс значения, отрезается перед сравнением.

ALTER TABLE nodes
    ADD COLUMN webhook_signature_header VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN webhook_signature_prefix VARCHAR(64)  NOT NULL DEFAULT 'sha256=';

-- Расширяем CHECK по incoming_auth_type. Постгрес не умеет ALTER CONSTRAINT,
-- поэтому drop + add.
ALTER TABLE nodes DROP CONSTRAINT nodes_inc_auth_check;
ALTER TABLE nodes ADD  CONSTRAINT nodes_inc_auth_check
    CHECK (incoming_auth_type IN ('none', 'basic', 'token', 'webhook_signature'));

-- Технический guard: для webhook_signature header не может быть пустым.
ALTER TABLE nodes ADD CONSTRAINT nodes_webhook_sig_header_required
    CHECK (incoming_auth_type <> 'webhook_signature' OR char_length(webhook_signature_header) > 0);
