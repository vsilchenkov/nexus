-- 0011_host_allowlist.up.sql
--
-- §23 ТЗ: общий каталог разрешённых хостов (защита от SSRF для
-- url_mode=from_request). До этой миграции allowlist жил только как
-- денормализованный массив nodes.url_allowed_hosts TEXT[] (миграция 0002).
--
-- Вводим:
--   host_allowlist      — каталог паттернов (exact/wildcard/regex), общий для
--                         инсталляции, переиспользуется между узлами;
--   node_allowed_hosts  — many-to-many привязка паттернов к узлам;
--   trigger             — поддерживает денормализованный host_allowlist.usage_count
--                         (чтение дешевле, чем COUNT(*) на каждый рендер таблицы).
--
-- ВАЖНО: nodes.url_allowed_hosts ОСТАЁТСЯ как денормализованный снимок паттернов
-- привязанных хостов — Receiver читает его из JSON-кеша узла и не трогается этой
-- миграцией. Каталог = source of truth для управления; Web синхронизирует снимок
-- при привязке/отвязке. Regex-паттерны кодируются в снимок с префиксом 're:'.

CREATE TABLE host_allowlist (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    pattern     TEXT         NOT NULL,
    kind        VARCHAR(16)  NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    usage_count INTEGER      NOT NULL DEFAULT 0,
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT host_allowlist_kind_check  CHECK (kind IN ('exact', 'wildcard', 'regex')),
    CONSTRAINT host_allowlist_pattern_len CHECK (char_length(pattern) BETWEEN 1 AND 512),
    CONSTRAINT host_allowlist_desc_len    CHECK (char_length(description) <= 500),
    CONSTRAINT host_allowlist_usage_nonneg CHECK (usage_count >= 0)
);

-- Уникальность паттерна без учёта регистра (api.PARTNER.com == api.partner.com).
CREATE UNIQUE INDEX host_allowlist_pattern_lower_uniq ON host_allowlist (lower(pattern));

-- Many-to-many: у одного хоста может быть много узлов, у узла — до 50 хостов.
-- ON DELETE RESTRICT на host_id — нельзя удалить используемый паттерн (второй
-- уровень защиты поверх usage_count-guard в usecase).
CREATE TABLE node_allowed_hosts (
    node_id    UUID        NOT NULL REFERENCES nodes(id)          ON DELETE CASCADE,
    host_id    UUID        NOT NULL REFERENCES host_allowlist(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, host_id)
);
CREATE INDEX node_allowed_hosts_host_id_idx ON node_allowed_hosts (host_id);

-- Денормализованный usage_count: trigger на каждую привязку/отвязку.
-- При удалении узла ON DELETE CASCADE снимает строки → DELETE-ветка декрементит.
CREATE OR REPLACE FUNCTION host_allowlist_bump_usage() RETURNS TRIGGER AS $$
BEGIN
    IF (TG_OP = 'INSERT') THEN
        UPDATE host_allowlist SET usage_count = usage_count + 1 WHERE id = NEW.host_id;
    ELSIF (TG_OP = 'DELETE') THEN
        UPDATE host_allowlist SET usage_count = usage_count - 1 WHERE id = OLD.host_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER node_allowed_hosts_usage_trg
    AFTER INSERT OR DELETE ON node_allowed_hosts
    FOR EACH ROW EXECUTE FUNCTION host_allowlist_bump_usage();
