-- 0042_node_groups.up.sql
--
-- §99 ТЗ: справочник групп узлов и поле группы у узла. Экран «Узлы» (§7.3)
-- показывает узлы без группы сверху (как сегодня), ниже — секции по группам в
-- порядке sort_order; существующая сортировка §22.5 («проблемные первыми»)
-- начинает действовать внутри секции.
--
-- Справочник ГЛОБАЛЬНЫЙ (без team_id) — как host_allowlist (§23),
-- headers_catalog (§24), request_fields_catalog (§41), log_mask_patterns (§95).
-- Одна и та же прикладная группа («1С Обмен», «Маркетплейсы») встречается в
-- разных командах, а в сквозном режиме §86 команднозависимый справочник дал бы
-- на одном экране несколько разных групп с одинаковым именем.
--
-- uuid_generate_v4(), а НЕ gen_random_uuid(): на боевом PostgreSQL 12 вторая не
-- встроена (появилась в PG 13), а в схеме включён только uuid-ossp (0001).

CREATE TABLE node_groups (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- Имя группы — СВОБОДНЫЙ текст (в отличие от headers_catalog, где CHECK
    -- держит RFC 7230 token): группы называет человек, «1С Обмен» и
    -- «Курьерские службы» обязаны проходить. Пробелы по краям обрезает usecase.
    name        VARCHAR(100) NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    -- Порядок вывода групп на экране «Узлы»: МЕНЬШЕ — ВЫШЕ. Шкала разрежённая
    -- ((позиция+1)*10 при сдвиге стрелками), чтобы оставалось место для ручной
    -- вставки между группами.
    sort_order  INTEGER      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- §63: автор/редактор (Actor.UserLogin).
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    updated_by  VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT node_groups_name_len   CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT node_groups_desc_len   CHECK (char_length(description) <= 500),
    CONSTRAINT node_groups_sort_range CHECK (sort_order BETWEEN 0 AND 100000)
);

-- Уникальность имени без учёта регистра («1С обмен» == «1С Обмен»).
CREATE UNIQUE INDEX node_groups_name_lower_uniq ON node_groups (lower(name));

-- Основной порядок чтения справочника: ORDER BY sort_order, name.
CREATE INDEX node_groups_order_idx ON node_groups (sort_order, name);

-- Группа узла. NULL = «без группы» (такие узлы на экране идут первыми и без
-- заголовка секции).
--
-- Ссылка по id, а НЕ по имени строкой (headers_catalog так не умеет — там узел
-- хранит имена в nodes.forward_headers). Следствие: переименование группы
-- ничего не осиротит и разрешено всегда; блокируется только УДАЛЕНИЕ
-- используемой группы.
--
-- ON DELETE RESTRICT — второй уровень защиты к guard-проверке usecase
-- (usage_count > 0 → ErrNodeGroupInUse): удалить используемую группу не даст и
-- СУБД, даже если запись создана в обход usecase.
ALTER TABLE nodes ADD COLUMN group_id UUID NULL REFERENCES node_groups(id) ON DELETE RESTRICT;

-- Под счётчик использования on-read (COUNT(*) FROM nodes WHERE group_id = ...)
-- и под группировку списка. Частичный: узлов без группы может быть большинство,
-- и они в этот индекс не попадают.
CREATE INDEX nodes_group_id_idx ON nodes (group_id) WHERE group_id IS NOT NULL;
