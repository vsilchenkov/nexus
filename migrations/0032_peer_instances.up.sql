-- 0032_peer_instances.up.sql
--
-- §73 (реестр инстансов): список ДРУГИХ развёртываний Nexus, чтобы оператор
-- видел соседние ноды и мог быстро в них перейти из интерфейса.
--
-- Почему отдельная таблица, а не instance_identity (§70, миграция 0029): та —
-- синглтон (id=1) и описывает ИДЕНТИЧНОСТЬ САМОЙ ноды (её instance.id, факт
-- захвата БД ClickHouse). Здесь — наоборот, справочник ЧУЖИХ инстансов, и он
-- у каждой ноды свой. Смешивать нельзя: у instance_identity ровно одна строка
-- по построению, и любой её рост ломает стартовые гейты §70.5.
--
-- Свой инстанс в таблице НЕ хранится: его версия видна в футере сайдбара, код —
-- бейджем в шапке (§70.8). Строка «сам себя» плодила бы вопрос «а что если
-- адрес разошёлся с реальным» и требовала бы синхронизации ни с чем.
--
-- team_id намеренно НЕТ. Инстанс — инфраструктура целиком, а не ресурс команды:
-- раздел админский, и «инстанс команды vika» не имеет смысла (у соседней ноды
-- свой PostgreSQL и свой список команд). Ср. teams/users — они тоже без team_id.
--
-- Кеш последней пробы (last_*) хранится, чтобы таблица рисовалась мгновенно при
-- открытии вкладки, до того как отработает опрос. Это не источник истины о
-- живости — только «что было в прошлый раз»; актуальность даёт время
-- last_checked_at рядом со статусом. Побочная польза: видно, когда сосед
-- отвечал последний раз, без открытой страницы.
--
-- Секретов в таблице нет: проба ходит на публичные GET /api/version и GET /ready
-- соседа (§73.3), токен не нужен. Поэтому шифрование (как у кредов узлов в
-- adapter/out/postgres) здесь не применяется — шифровать нечего.
--
-- Совместимость: боевой сервер работает на PostgreSQL 12, поэтому синтаксис 13+
-- (UNIQUE NULLS NOT DISTINCT, gen_random_uuid без расширения и т.п.) запрещён.
-- uuid_generate_v4() доступна: расширение "uuid-ossp" создаётся в 0001.

CREATE TABLE peer_instances (
    id       UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    title    VARCHAR(64)  NOT NULL,
    base_url VARCHAR(255) NOT NULL,
    comment  VARCHAR(255) NOT NULL DEFAULT '',

    -- Результат последней пробы. status зеркалит domain.PeerInstanceStatus;
    -- 'unknown' = инстанс заведён, но ещё ни разу не опрашивался.
    last_status      VARCHAR(16)  NOT NULL DEFAULT 'unknown',
    last_version     VARCHAR(64)  NOT NULL DEFAULT '',
    -- Код инстанса соседа (instance.id из его /api/version). Пустая строка —
    -- валидное значение: у ноды без суффикса идентификатор пуст (§70.1),
    -- поэтому NULL здесь не нужен и только плодил бы трёхзначную логику.
    last_instance_id VARCHAR(16)  NOT NULL DEFAULT '',
    -- NULL, а не 0: «не измеряли» и «ответил за 0 мс» — разные состояния.
    last_latency_ms  INTEGER,
    -- Короткая нормализованная причина отказа ('timeout', 'http 502', ...).
    -- Сырое тело ответа и текст ошибки транспорта сюда НЕ попадают: они могут
    -- нести адреса и куски конфигурации соседа (урок §68 — стандартные ошибки
    -- вклеивают в текст сырой вход).
    last_error       VARCHAR(255) NOT NULL DEFAULT '',
    last_checked_at  TIMESTAMPTZ,

    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- Логины автора создания/изменения (приём §63). Не FK на users: запись
    -- должна пережить удаление пользователя, как и снапшоты автора у узлов.
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    updated_by VARCHAR(255) NOT NULL DEFAULT '',

    -- Зеркало domain.PeerInstance.Validate(): БД — последний рубеж.
    CONSTRAINT peer_instances_title_len CHECK (char_length(title) BETWEEN 1 AND 64),
    CONSTRAINT peer_instances_comment_len CHECK (char_length(comment) <= 255),
    -- Схема только http/https, обязателен хост, запрещены userinfo ('@' до
    -- первого '/') и путь после хоста. Точную проверку делает Validate() через
    -- url.Parse; CHECK ловит грубые случаи и прямые INSERT мимо приложения.
    CONSTRAINT peer_instances_base_url_fmt
        CHECK (base_url ~ '^https?://[^/@?#]+$'),
    CONSTRAINT peer_instances_last_status_enum
        CHECK (last_status IN ('unknown', 'active', 'degraded', 'unreachable', 'error')),
    CONSTRAINT peer_instances_last_latency_nonneg
        CHECK (last_latency_ms IS NULL OR last_latency_ms >= 0)
);

-- Один и тот же адрес нельзя завести дважды: две строки на один инстанс дают
-- два разных «последних статуса» одного и того же соседа. Регистр хоста в URL
-- не значим, поэтому сравнение по lower().
CREATE UNIQUE INDEX peer_instances_base_url_uniq ON peer_instances (lower(base_url));

-- Отдельный индекс под чтение не нужен: единственный SELECT — полный список
-- с сортировкой по title, а таблица заведомо мала (единицы строк).
