-- 0036_one_time_tokens.up.sql
--
-- §88.4.1 ТЗ: одноразовые ссылки, отправляемые пользователю письмом.
--
-- Таблица ОБЩАЯ, а не под один сценарий: восстановление пароля — лишь первый
-- потребитель, следующие названы в §88.12 (подтверждение смены email,
-- приглашение пользователя). Инварианты у всех одни — случайный токен,
-- хранение хеша, одноразовость, срок жизни, привязка к пользователю, предел
-- активных ссылок; различаются только назначение (purpose) и полезная
-- нагрузка (payload).
--
-- CHECK на purpose — по образцу users.role (0013/0035): опечатка в назначении
-- иначе выдавала бы токены, которые молча никогда не найдутся. Цена — новое
-- назначение требует миграции, ровно как новая роль.
--
-- uuid_generate_v4(), а НЕ gen_random_uuid(): на боевой PostgreSQL 12
-- последняя не встроена (появилась в PG 13) и требовала бы pgcrypto, а в
-- схеме включён только uuid-ossp (0001_init_schema).

CREATE TABLE one_time_tokens (
    id          UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose     VARCHAR(32)  NOT NULL,
    -- SHA-256 hex (64 символа), как api_tokens (0004): сам токен в БД не
    -- попадает — дамп базы не даёт возможности воспользоваться ссылкой.
    token_hash  VARCHAR(64)  NOT NULL UNIQUE,
    -- Данные назначения: у password_reset пусто; email_change положит сюда
    -- новый адрес, user_invite — роль и команду.
    payload     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ  NOT NULL,
    -- NULL = ссылка ещё не использована. Отдельная колонка, а не удаление
    -- строки: позволяет отличить «истекла» от «уже использована».
    used_at     TIMESTAMPTZ,
    request_ip  INET,
    CONSTRAINT one_time_tokens_purpose_check
        CHECK (purpose IN ('password_reset'))
);

-- Под «не более N активных ОДНОГО НАЗНАЧЕНИЯ на пользователя» и под гашение
-- всех выданных ссылок при смене пароля: обе операции смотрят только
-- непогашенные строки.
CREATE INDEX one_time_tokens_active_idx
    ON one_time_tokens (user_id, purpose) WHERE used_at IS NULL;

-- Под ленивую чистку просроченных (хвост 7 дней, §88.4.1).
CREATE INDEX one_time_tokens_expires_idx ON one_time_tokens (expires_at);

-- Отдельного индекса по token_hash не нужно: инлайновый UNIQUE уже создаёт его.

COMMENT ON TABLE one_time_tokens IS
    'Single-use action links per §88.4 — token value never stored (SHA-256 only)';

-- §88.4.2: резолв пользователя по email в потоке восстановления.
-- НЕ UNIQUE — в существующих данных дубли возможны, и UNIQUE не дал бы
-- миграции примениться; неоднозначный адрес трактуется как «не найден».
CREATE INDEX users_email_lower_idx ON users (lower(email)) WHERE email IS NOT NULL;
