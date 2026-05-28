-- Phase F1.2 (§19): глобальный каталог шаблонов CREATE TABLE для таблиц логов
-- узла. spec (JSONB) — структурное описание (engine/partition/order/codecs/
-- indexes/ttl), из которого детерминированно генерируется DDL (см.
-- domain.CHTemplate.RenderCreateTable). Узлы ссылаются на шаблон через
-- nodes.clickhouse_template_id (NULL = ручная таблица, legacy-поведение).

CREATE TABLE ch_templates (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(64)  NOT NULL UNIQUE,
    description TEXT         NOT NULL DEFAULT '',
    spec        JSONB        NOT NULL,
    is_default  BOOLEAN      NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT ch_templates_name_format CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9 _-]{0,63}$')
);

-- Не более одного шаблона по умолчанию.
CREATE UNIQUE INDEX ch_templates_one_default ON ch_templates (is_default) WHERE is_default;

-- Сид «Standard logs» — рендерится ровно в схему §4.3 (DefaultCHTemplateSpec).
INSERT INTO ch_templates (name, description, spec, is_default) VALUES (
    'Standard logs',
    'Default node log schema (MergeTree, monthly partitions, §4.3).',
    '{"engine":"MergeTree","partition_by":"toYYYYMM(date_create)","order_by":["date_create","date_request","method"],"ttl_mode":"none"}'::jsonb,
    true
);

ALTER TABLE nodes
    ADD COLUMN clickhouse_template_id UUID REFERENCES ch_templates(id) ON DELETE SET NULL;
