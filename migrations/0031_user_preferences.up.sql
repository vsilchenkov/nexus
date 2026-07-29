-- 0031_user_preferences.up.sql
--
-- §71 (персональные предпочтения): generic-хранилище пользовательских настроек
-- UI — per-user, опционально per-team. Первый ключ — 'overview.period'
-- (дефолтный период рабочего стола «под себя», §44.B): раньше он жил в
-- localStorage браузера, был один на все команды пользователя и не переезжал
-- между устройствами. Будущие префы (§48 «сохранённые фильтры per-user»)
-- ложатся сюда новым key — без новой миграции и без правки бэкенда.
--
-- team_id NULL     = глобальный преф пользователя (действует во всех командах);
-- team_id NOT NULL = преф в контексте конкретной команды, перекрывает глобальный.
-- Двухуровневый резолв (команда → глобальный → системный дефолт) делает клиент:
-- сервер значение не интерпретирует вовсе (см. domain.UserPreference.Validate).
--
-- Уникальность — индекс по ВЫРАЖЕНИЮ с COALESCE, а не UNIQUE-констрейнт по
-- колонкам: с обычным UNIQUE две строки (user, NULL, 'overview.period')
-- считались бы различными (NULL <> NULL), в таблице копились бы дубли
-- глобальных префов, а ON CONFLICT для них не срабатывал бы вовсе — upsert
-- превратился бы в append.
--
-- Просится UNIQUE NULLS NOT DISTINCT, но это PostgreSQL 15+, а боевой сервер
-- работает на 12 — там такая миграция не применится и сервис не поднимется.
-- COALESCE-индекс даёт ту же семантику на любой поддерживаемой версии; колонка
-- при этом остаётся nullable, поэтому составной FK на user_teams продолжает
-- работать (см. ниже). Нулевой UUID выбран как значение-заглушка: в teams его
-- быть не может, потому что id генерируется случайным.
--
-- Два FK намеренно, они закрывают разные строки:
--   * (user_id, team_id) -> user_teams — приём §49 (user_team_favorites): один
--     каскад покрывает удаление пользователя, удаление команды и исключение из
--     членства (RemoveMember удаляет строку user_teams напрямую) и даёт
--     БД-инвариант «преф ⊆ членство»;
--   * user_id -> users — каскад для ГЛОБАЛЬНЫХ строк (team_id IS NULL), которые
--     составной FK по правилу MATCH SIMPLE не проверяет и, соответственно, не
--     удаляет.
--
-- Отдельный индекс под чтение не нужен: уникальный индекс ниже начинается с
-- user_id — он обслуживает и единственный SELECT (WHERE user_id = $1), и обе
-- ветки upsert'а.
CREATE TABLE user_preferences (
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    team_id    UUID,
    key        VARCHAR(64) NOT NULL,
    value      JSONB       NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT user_preferences_membership_fkey
        FOREIGN KEY (user_id, team_id)
        REFERENCES user_teams (user_id, team_id) ON DELETE CASCADE,

    -- Формат ключа — namespace'ы через точку ('overview.period', 'logs.filters').
    -- Зеркалит domain.UserPreference.Validate(): БД — последний рубеж.
    CONSTRAINT user_preferences_key_format
        CHECK (key ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$'),

    -- JSON null запрещён: «преф есть, но значения нет» — состояние без смысла,
    -- отсутствие префа выражается отсутствием строки.
    CONSTRAINT user_preferences_value_not_null
        CHECK (jsonb_typeof(value) <> 'null'),

    -- Потолок значения: преф — настройка UI, а не документ. 4 КиБ хватает и под
    -- §48 (сохранённый набор фильтров). Дублирует лимит из Validate().
    CONSTRAINT user_preferences_value_size
        CHECK (octet_length(value::text) <= 4096)
);

-- Ровно один преф на (пользователь, команда, ключ); строки без команды
-- (team_id IS NULL) сравниваются между собой как равные — на этом держится
-- upsert глобального префа (ON CONFLICT в user_preferences.go указывает то же
-- самое выражение).
CREATE UNIQUE INDEX user_preferences_uniq
    ON user_preferences (
        user_id,
        COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid),
        key
    );
