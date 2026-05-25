-- 0004_api_tokens.up.sql
--
-- §5.1 + §7.14 ТЗ. Read-only API-токены для интеграций.

CREATE TABLE api_tokens (
    id           UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id      UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         VARCHAR(255) NOT NULL,
    token_hash   VARCHAR(64)  NOT NULL UNIQUE,  -- SHA-256 hex (64 chars)
    prefix       VARCHAR(16)  NOT NULL,         -- "db_xxxxxxxx" — первые 8 символов плюс префикс
    scopes       TEXT[]       NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX api_tokens_token_hash_idx ON api_tokens (token_hash);
CREATE INDEX api_tokens_user_id_idx    ON api_tokens (user_id);

COMMENT ON TABLE api_tokens IS 'Read-only API tokens per §7.14 — token value never stored (SHA-256 only)';
