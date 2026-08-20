-- 0040_rejected_requests.up.sql
--
-- §94 ТЗ: журнал запросов, которые Receiver не принял (несуществующий узел,
-- недопустимый метод, отсутствие авторизации, превышение лимитов).
--
-- Хранение АГРЕГИРОВАННОЕ, а не построчное: ключ группы схлопывает поток, и
-- сканер на 1000 запросов в секунду даёт один UPDATE раз в интервал сброса
-- вместо миллиона вставок. Детализация «кто именно» живёт в rejected_clients,
-- «как именно выглядел запрос» — в rejected_samples с жёстким лимитом строк.
--
-- uuid_generate_v4(), а НЕ gen_random_uuid(): на боевом PostgreSQL 12 вторая не
-- встроена (появилась в PG 13), а в схеме включён только uuid-ossp (0001).

CREATE TABLE rejected_groups (
    id           UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- Слог команды из URL. Хранится КАК ПРИШЁЛ и может не соответствовать ни
    -- одной существующей команде: при 404 узла нет, значит нет и надёжного
    -- способа определить команду. Пустая строка — форма адреса без слога
    -- (§78.1), она трактуется как default. FK на teams нет намеренно.
    team_slug    VARCHAR(64)  NOT NULL DEFAULT '',
    -- Канонический путь узла (когда узел нашёлся) либо путь из URL (когда нет).
    -- Обрезается писателем до 512 символов: путь задаёт клиент, а колонка
    -- входит в уникальный ключ и в индекс.
    node_path    VARCHAR(512) NOT NULL,
    reason       VARCHAR(32)  NOT NULL,
    http_method  VARCHAR(16)  NOT NULL,
    -- HTTP-код ответа клиенту. Отдельно от причины: интерфейсу нужна пара
    -- целиком, восстанавливать код по причине в каждом месте показа — лишнее.
    status       SMALLINT     NOT NULL,
    first_seen   TIMESTAMPTZ  NOT NULL,
    last_seen    TIMESTAMPTZ  NOT NULL,
    count        BIGINT       NOT NULL DEFAULT 0,
    -- Денормализация: число строк в rejected_clients. Считать подзапросом на
    -- каждую строку выдачи дороже, чем обновить при сбросе буфера.
    clients      INTEGER      NOT NULL DEFAULT 0,
    -- NULL = группа не разобрана. Отметка снимается автоматически при новом
    -- отказе (§94.6): иначе однажды закрытая группа скрывала бы возобновившуюся
    -- проблему.
    resolved_at  TIMESTAMPTZ,
    resolved_by  VARCHAR(64),
    -- CHECK по образцу users.role (0013/0035): опечатка в причине иначе
    -- создавала бы группы, которые не найдёт ни один фильтр интерфейса.
    CONSTRAINT rejected_groups_reason_enum CHECK (reason IN (
        'node_not_found', 'method_not_allowed', 'unauthorized', 'node_disabled',
        'url_not_allowed', 'body_too_large', 'rate_limited', 'loop_detected',
        'bad_request', 'other')),
    -- Естественный ключ группы: писатель делает по нему UPSERT из нескольких
    -- реплик Receiver одновременно.
    CONSTRAINT rejected_groups_key UNIQUE (team_slug, node_path, reason, http_method)
);

-- Выдача всегда сортируется по last_seen DESC: и общий список (администратор),
-- и список одной команды. Второй индекс ведёт с team_slug, чтобы страница
-- «редкой» команды не просматривала чужие строки.
CREATE INDEX rejected_groups_last_seen_idx ON rejected_groups (last_seen DESC);
CREATE INDEX rejected_groups_team_last_seen_idx ON rejected_groups (team_slug, last_seen DESC);

CREATE TABLE rejected_clients (
    group_id    UUID         NOT NULL REFERENCES rejected_groups(id) ON DELETE CASCADE,
    client_ip   INET         NOT NULL,
    -- PTR-имя (§67). Пусто = нет записи, резолв не удался или адрес не IP.
    client_host VARCHAR(255) NOT NULL DEFAULT '',
    user_agent  VARCHAR(256) NOT NULL DEFAULT '',
    count       BIGINT       NOT NULL DEFAULT 0,
    first_seen  TIMESTAMPTZ  NOT NULL,
    last_seen   TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (group_id, client_ip)
);

-- Под вытеснение самого давнего клиента при переполнении лимита группы
-- (50 адресов) и под сортировку списка клиентов в карточке.
CREATE INDEX rejected_clients_group_last_seen_idx ON rejected_clients (group_id, last_seen DESC);

CREATE TABLE rejected_samples (
    id          BIGSERIAL    PRIMARY KEY,
    group_id    UUID         NOT NULL REFERENCES rejected_groups(id) ON DELETE CASCADE,
    at          TIMESTAMPTZ  NOT NULL,
    client_ip   INET         NOT NULL,
    http_method VARCHAR(16)  NOT NULL,
    -- Полный путь запроса; значения query замаскированы писателем (§94.9).
    raw_path    VARCHAR(1024) NOT NULL,
    status      SMALLINT     NOT NULL,
    -- Размер тела в байтах. Само тело не хранится НИКОГДА.
    body_bytes  BIGINT       NOT NULL DEFAULT 0,
    request_id  VARCHAR(64)  NOT NULL DEFAULT '',
    -- Отобранный белый список заголовков (§94.9), не всё подряд.
    headers     JSONB        NOT NULL DEFAULT '{}'::jsonb
);

-- Под выдачу «последние N запросов группы» и под удаление лишних сверх лимита.
CREATE INDEX rejected_samples_group_at_idx ON rejected_samples (group_id, at DESC);
