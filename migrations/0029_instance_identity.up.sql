-- 0029_instance_identity.up.sql
--
-- §70 (несколько нод Nexus на одном ClickHouse): нода — самостоятельное
-- развёртывание со своими PostgreSQL/Redis/Kafka, пишущее логи в ОБЩИЙ с
-- другими нодами ClickHouse. Идентификатор ноды задаётся в конфиге
-- (instance.id) и фиксируется здесь — в её собственной базе.
--
-- Строку НЕ сидируем: её отсутствие означает «идентификатор ещё не заявлен»,
-- то есть первый запуск ноды. Заявку делает первый стартовавший сервис
-- (INSERT ... ON CONFLICT DO NOTHING — гонка трёх сервисов безопасна, значение
-- у них одно и то же из конфига). При расхождении сохранённого значения с
-- конфигом сервис не стартует: смена идентификатора не переименовывает уже
-- созданные БД ClickHouse (§70.5).
CREATE TABLE IF NOT EXISTS instance_identity (
    id          INTEGER     PRIMARY KEY DEFAULT 1,
    instance_id VARCHAR(16) NOT NULL,
    claimed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_by  VARCHAR(64) NOT NULL DEFAULT '',
    CONSTRAINT instance_identity_singleton CHECK (id = 1),
    CONSTRAINT instance_identity_format
        CHECK (instance_id = '' OR instance_id ~ '^[a-z][a-z0-9]{0,7}$')
);

-- §70.2: хвост имени БД команды расширяется с 31 до 40 символов.
-- Имя строится как "nexus_" + [<instance_id>_] + <slug>, а слаг сам допускает
-- 32 символа (teams_slug_format) — с суффиксом ноды прежнее ограничение
-- отвергало бы длинные слаги с невнятной ошибкой про ch_database, хотя
-- оператор задаёт только слаг. 6 + 41 <= VARCHAR(64), запас сохраняется.
ALTER TABLE teams DROP CONSTRAINT IF EXISTS teams_ch_database_format;
ALTER TABLE teams ADD CONSTRAINT teams_ch_database_format
    CHECK (ch_database ~ '^nexus_[a-z][a-z0-9_]{0,40}$');
