-- 0012_headers_catalog.up.sql
--
-- §24 ТЗ: общий справочник HTTP-заголовков для combobox-автодополнения в
-- секции «Проброс заголовков» формы узла. Каталог общий для инсталляции.
--
-- usage_count НЕ хранится: привязка заголовка к узлу живёт в денормализованном
-- массиве nodes.forward_headers (его читает Receiver), отдельной M2M-таблицы
-- нет. Счётчик использования считается on-read как
-- COUNT(*) FROM nodes WHERE name = ANY(forward_headers) — это исключает класс
-- рассинхрона trigger'а на TEXT[].

CREATE TABLE headers_catalog (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(100) NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT headers_catalog_name_len  CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT headers_catalog_desc_len  CHECK (char_length(description) <= 500),
    -- RFC 7230 token: латиница, цифры и допустимые спецсимволы (без пробелов).
    CONSTRAINT headers_catalog_name_fmt  CHECK (name ~ '^[a-zA-Z0-9!#$%&''*+.^_`|~-]+$')
);

-- Уникальность имени без учёта регистра (X-Request-ID == x-request-id).
CREATE UNIQUE INDEX headers_catalog_name_lower_uniq ON headers_catalog (lower(name));
