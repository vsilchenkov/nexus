-- 0041_log_mask_patterns.up.sql
--
-- §95 ТЗ: справочник regex-шаблонов, по которым Sender маскирует секреты в
-- логах узлов (ClickHouse) ПЕРЕД записью. Закрывает случай, который не ловят ни
-- platform/sensitive (маскирует по именам полей), ни redactURL (только query):
-- секрет пришёл от клиента В ПУТИ (токен Telegram-бота
-- POST /api/v1/telegram/bot<ТОКЕН>/getUpdates, waInstance/ключ у green-api) и
-- оседает открытым текстом в колонках url и method (§39, RequestPath).
--
-- Шаблоны ГЛОБАЛЬНЫЕ на инсталляцию (без team-скоупа): маскирование — свойство
-- всего лога, а не команды. Порядок применения — sort_order по возрастанию,
-- затем created_at (детерминизм при равных sort_order). Замена поддерживает
-- группы Go regexp ($1..$9 / ${name}), чтобы не терять смысл лога
-- ((bot\d+):… → $1:*** оставляет bot123456:***).
--
-- uuid_generate_v4(), а НЕ gen_random_uuid(): на боевом PostgreSQL 12 вторая не
-- встроена (появилась в PG 13), а в схеме включён только uuid-ossp (0001).

CREATE TABLE log_mask_patterns (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- RE2-регэксп (Go regexp). Компилируемость проверяет usecase на create/update
    -- (CHECK по длине не гарантирует валидность синтаксиса).
    pattern     TEXT         NOT NULL,
    -- Строка замены совпавшего фрагмента; поддерживает $1..$9 и ${name}.
    replacement TEXT         NOT NULL DEFAULT '***',
    description TEXT         NOT NULL DEFAULT '',
    -- Выключенный шаблон остаётся в справочнике, но не применяется (reader
    -- Sender'а фильтрует по enabled). Позволяет отключить правило, не удаляя.
    enabled     BOOLEAN      NOT NULL DEFAULT true,
    -- Порядок применения: по возрастанию. Замены накладываются последовательно,
    -- поэтому порядок значим (одно правило может подготовить вход другому).
    sort_order  INTEGER      NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- §63: автор/редактор (Actor.UserLogin). Пусто — сид миграции.
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    updated_by  VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT log_mask_pattern_len       CHECK (char_length(pattern) BETWEEN 1 AND 500),
    CONSTRAINT log_mask_replacement_len   CHECK (char_length(replacement) <= 200),
    CONSTRAINT log_mask_description_len    CHECK (char_length(description) <= 500),
    CONSTRAINT log_mask_sort_order_nonneg CHECK (sort_order >= 0)
);

-- Под чтение активных шаблонов в порядке применения (reader Sender'а):
-- WHERE enabled ORDER BY sort_order, created_at. Таблица мала, индекс скорее
-- документирует паттерн чтения, чем ускоряет.
CREATE INDEX log_mask_patterns_apply_idx ON log_mask_patterns (enabled, sort_order, created_at);

-- Сиды закрывают известные утечки (бой nexus-kz: узлы telegram / green-whatsapp
-- с path_passthrough). Регэкспы намеренно широкие по «хвосту» токена и узкие по
-- префиксу, чтобы не задеть полезные данные; оператор проверяет их превью в UI и
-- правит под свои узлы.
INSERT INTO log_mask_patterns (pattern, replacement, description, sort_order) VALUES
    -- Telegram bot-токен в пути: bot<digits>:<token>. Матчит и url
    -- (https://api.telegram.org/bot123:AAE…/getUpdates), и method (§39,
    -- bot123:AAE…/getUpdates). Двоеточие и id сохраняются — видно, что это бот.
    ('(bot[0-9]{6,}):[A-Za-z0-9_-]{20,}', '$1:***', 'Telegram bot token in path', 10),
    -- green-api: waInstance<id>/<method>/<token>. Сохраняем инстанс и метод,
    -- маскируем последний сегмент-токен.
    ('(waInstance[0-9]+/[A-Za-z]+/)[A-Za-z0-9._-]{20,}', '$1***', 'green-api API token in path', 20);
