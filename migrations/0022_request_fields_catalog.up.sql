-- 0022_request_fields_catalog.up.sql
--
-- §41: общий справочник «полей запроса» для combobox-автодополнения в секциях
-- авторизации формы узла. «Поле запроса» — имя HTTP-заголовка или query-
-- параметра, откуда берётся креда для динамической авторизации: исходящей
-- (token/basic_from_request → auth_dynamic_field) и входящей (token/basic →
-- incoming_auth_dynamic_field). Каталог общий для инсталляции (как §24
-- заголовки).
--
-- usage_count НЕ хранится: имя поля живёт в денормализованных колонках
-- nodes.auth_dynamic_field / nodes.incoming_auth_dynamic_field; счётчик
-- считается on-read (COUNT узлов с этим именем в любой из двух колонок).

CREATE TABLE request_fields_catalog (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(64)  NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT request_fields_name_len CHECK (char_length(name) BETWEEN 1 AND 64),
    CONSTRAINT request_fields_desc_len CHECK (char_length(description) <= 500),
    -- Имя HTTP-заголовка / query-параметра (то же ограничение, что у
    -- nodes.auth_dynamic_field): латиница на старте, далее буквы/цифры/дефис/_.
    CONSTRAINT request_fields_name_fmt CHECK (name ~ '^[a-zA-Z][a-zA-Z0-9_-]*$')
);

-- Уникальность имени без учёта регистра (Authorization == authorization).
CREATE UNIQUE INDEX request_fields_name_lower_uniq ON request_fields_catalog (lower(name));
