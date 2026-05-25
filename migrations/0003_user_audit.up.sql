-- 0003_user_audit.up.sql
--
-- §5.1 + §7.13 ТЗ. Append-only журнал действий пользователей.

CREATE TABLE user_audit (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID         NULL,
    user_login  VARCHAR(255) NOT NULL,
    action      VARCHAR(64)  NOT NULL,
    target_type VARCHAR(64)  NOT NULL DEFAULT '',
    target_id   VARCHAR(255) NOT NULL DEFAULT '',
    details     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    ip_address  INET         NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX user_audit_created_at_idx     ON user_audit (created_at DESC);
CREATE INDEX user_audit_user_id_idx        ON user_audit (user_id);
CREATE INDEX user_audit_action_idx         ON user_audit (action);
CREATE INDEX user_audit_target_idx         ON user_audit (target_type, target_id);

COMMENT ON TABLE user_audit IS 'Append-only audit log per §7.13 — modify/delete prohibited at application level';
