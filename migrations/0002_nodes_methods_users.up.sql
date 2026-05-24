-- 0002_nodes_methods_users.up.sql
--
-- §5.1 ТЗ — таблицы конфигурации шины.
-- В Phase 1 создаём минимум для sync-пути: methods, nodes, node_headers, users.
-- user_audit, api_tokens, app_settings, clickhouse_settings — Phase 2/3.

-- Корневые методы — справочник, всегда два значения.
CREATE TABLE methods (
    name VARCHAR(32) PRIMARY KEY,
    CHECK (name IN ('request', 'requestAsync'))
);
INSERT INTO methods (name) VALUES ('request'), ('requestAsync');

-- Узлы перенаправления (§3.3).
CREATE TABLE nodes (
    id                          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    path                        VARCHAR(255) NOT NULL UNIQUE,
    root_method                 VARCHAR(32)  NOT NULL REFERENCES methods(name),

    url_mode                    VARCHAR(32)  NOT NULL DEFAULT 'static',
    target_url                  VARCHAR(2048) NOT NULL DEFAULT '',
    url_param_name              VARCHAR(64)  NOT NULL DEFAULT 'url_base',
    url_allowed_hosts           TEXT[]       NOT NULL DEFAULT '{}',

    auth_type                   VARCHAR(32)  NOT NULL DEFAULT 'none',
    auth_credentials            TEXT         NOT NULL DEFAULT '',  -- AES-256-GCM, формат v1:nonce:ct:tag (§5.5)
    auth_dynamic_source         VARCHAR(16)  NOT NULL DEFAULT 'query',
    auth_dynamic_field          VARCHAR(64)  NOT NULL DEFAULT 'token',
    auth_dynamic_strip_prefix   VARCHAR(64)  NOT NULL DEFAULT 'Bearer ',

    incoming_auth_type          VARCHAR(32)  NOT NULL DEFAULT 'none',
    incoming_auth_credentials   TEXT         NOT NULL DEFAULT '',  -- AES-256-GCM

    forward_headers             TEXT[]       NOT NULL DEFAULT '{}',
    timeout_ms                  INTEGER      NOT NULL DEFAULT 30000,
    retry_count                 INTEGER      NOT NULL DEFAULT 0,
    retry_backoff_ms            INTEGER      NOT NULL DEFAULT 1000,
    clickhouse_table            VARCHAR(129) NOT NULL DEFAULT '',

    status                      VARCHAR(16)  NOT NULL DEFAULT 'enabled',
    team_id                     VARCHAR(64)  NOT NULL DEFAULT 'default',  -- §16: задел под v2 multi-tenancy

    log_request_body            BOOLEAN      NOT NULL DEFAULT false,
    log_response_body           BOOLEAN      NOT NULL DEFAULT false,
    log_headers                 BOOLEAN      NOT NULL DEFAULT false,

    created_at                  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT nodes_url_mode_check     CHECK (url_mode IN ('static', 'from_request')),
    CONSTRAINT nodes_auth_type_check    CHECK (auth_type IN ('none', 'basic', 'token', 'token_from_request', 'basic_from_request')),
    CONSTRAINT nodes_auth_dyn_src_check CHECK (auth_dynamic_source IN ('query', 'header', 'body')),
    CONSTRAINT nodes_inc_auth_check     CHECK (incoming_auth_type IN ('none', 'basic', 'token')),
    CONSTRAINT nodes_status_check       CHECK (status IN ('enabled', 'disabled', 'paused')),
    CONSTRAINT nodes_path_format        CHECK (path ~ '^[a-zA-Z0-9][a-zA-Z0-9/_-]*$'),
    CONSTRAINT nodes_path_len           CHECK (char_length(path) BETWEEN 1 AND 255),
    CONSTRAINT nodes_target_url_len     CHECK (char_length(target_url) <= 2048),
    CONSTRAINT nodes_url_param_format   CHECK (url_param_name ~ '^[a-zA-Z][a-zA-Z0-9_-]*$'),
    CONSTRAINT nodes_url_param_len      CHECK (char_length(url_param_name) BETWEEN 1 AND 64),
    CONSTRAINT nodes_auth_dyn_fld_fmt   CHECK (auth_dynamic_field ~ '^[a-zA-Z][a-zA-Z0-9_-]*$'),
    CONSTRAINT nodes_auth_dyn_fld_len   CHECK (char_length(auth_dynamic_field) BETWEEN 1 AND 64),
    CONSTRAINT nodes_strip_prefix_len   CHECK (char_length(auth_dynamic_strip_prefix) <= 64),
    CONSTRAINT nodes_timeout_range      CHECK (timeout_ms BETWEEN 100 AND 300000),
    CONSTRAINT nodes_retry_count_range  CHECK (retry_count BETWEEN 0 AND 10),
    CONSTRAINT nodes_retry_backoff_range CHECK (retry_backoff_ms BETWEEN 0 AND 60000),
    CONSTRAINT nodes_ch_table_len       CHECK (char_length(clickhouse_table) <= 129),
    CONSTRAINT nodes_allowed_hosts_size CHECK (array_length(url_allowed_hosts, 1) IS NULL OR array_length(url_allowed_hosts, 1) <= 50),
    CONSTRAINT nodes_forward_hdr_size   CHECK (array_length(forward_headers, 1)  IS NULL OR array_length(forward_headers,  1) <= 30),
    CONSTRAINT nodes_static_needs_url   CHECK (url_mode != 'static' OR char_length(target_url) > 0)
);

CREATE INDEX nodes_status_idx  ON nodes (status);
CREATE INDEX nodes_team_id_idx ON nodes (team_id);

-- Пользователи UI (§7.1, §7.9).
CREATE TABLE users (
    id                       UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    login                    VARCHAR(255) NOT NULL UNIQUE,
    email                    VARCHAR(255),
    password_hash            VARCHAR(128),  -- bcrypt/argon2; NULL = ещё не задан (для дефолтного admin)
    role                     VARCHAR(32)  NOT NULL DEFAULT 'viewer',
    active                   BOOLEAN      NOT NULL DEFAULT true,
    must_change_password     BOOLEAN      NOT NULL DEFAULT false,
    lang                     VARCHAR(8)   NOT NULL DEFAULT 'en',
    team_id                  VARCHAR(64)  NOT NULL DEFAULT 'default',
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_login_at            TIMESTAMPTZ,
    CONSTRAINT users_role_check CHECK (role IN ('admin', 'viewer')),
    CONSTRAINT users_lang_check CHECK (lang IN ('en', 'ru')),
    CONSTRAINT users_login_len  CHECK (char_length(login) BETWEEN 1 AND 255)
);

-- Дефолтный администратор с пустым паролем (см. §7.1 — при первом входе forced change).
-- password_hash NULL => логин невозможен, пока админ не задаст пароль через CLI / UI.
INSERT INTO users (login, role, active, must_change_password)
VALUES ('admin', 'admin', true, true);
