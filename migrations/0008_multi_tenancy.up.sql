-- 0008_multi_tenancy.up.sql
--
-- Phase A.1 (multi-tenancy v2): первичная схема для команд.
--
-- До этой миграции team_id в nodes/users был строкой 'default' и служил
-- закладкой под v2 (§16 ТЗ). Сейчас вводим:
--
--   teams         — справочник команд, каждой соответствует своя БД ClickHouse;
--   user_teams    — many-to-many членство пользователей в командах с ролью;
--   nodes.team_id — UUID FK на teams (был VARCHAR);
--   users.default_team_id — UUID FK (бывший team_id, теперь означает «команда
--                            по умолчанию при логине», реальная видимость —
--                            через user_teams);
--   api_tokens.team_id    — UUID FK, токен ограничен одной командой;
--   user_audit.team_id    — UUID FK NULL (NULL = глобальное действие админа).
--
-- UNIQUE(path) на nodes снимается, ставится UNIQUE(team_id, path) — две
-- команды могут иметь свои узлы с одинаковым path.
--
-- Дефолтная команда 'default' (ch_database='nexus_default') сидится сразу;
-- существующий 'admin' юзер становится owner'ом этой команды.

-- 1. Справочник команд.
CREATE TABLE teams (
    id           UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    slug         VARCHAR(32)  NOT NULL UNIQUE,
    name         VARCHAR(255) NOT NULL,
    ch_database  VARCHAR(64)  NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT teams_slug_format       CHECK (slug ~ '^[a-z][a-z0-9_]{0,31}$'),
    CONSTRAINT teams_name_len          CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT teams_ch_database_format
        CHECK (ch_database ~ '^nexus_[a-z][a-z0-9_]{0,31}$')
);

INSERT INTO teams (slug, name, ch_database)
VALUES ('default', 'Default Team', 'nexus_default');

-- 2. nodes.team_id: VARCHAR(64) → UUID FK на teams.
ALTER TABLE nodes ADD COLUMN team_uuid UUID;
UPDATE nodes n
   SET team_uuid = (SELECT id FROM teams t WHERE t.slug = n.team_id);
-- На всякий случай: если по какой-то причине осталась запись без mapping —
-- цепляем к default. Гарантия NOT NULL после.
UPDATE nodes
   SET team_uuid = (SELECT id FROM teams WHERE slug = 'default')
 WHERE team_uuid IS NULL;
ALTER TABLE nodes ALTER COLUMN team_uuid SET NOT NULL;
ALTER TABLE nodes
    ADD CONSTRAINT nodes_team_id_fkey
    FOREIGN KEY (team_uuid) REFERENCES teams(id) ON DELETE RESTRICT;

DROP INDEX IF EXISTS nodes_team_id_idx;
ALTER TABLE nodes DROP COLUMN team_id;
ALTER TABLE nodes RENAME COLUMN team_uuid TO team_id;
CREATE INDEX nodes_team_id_idx ON nodes (team_id);

-- 3. UNIQUE(path) → UNIQUE(team_id, path).
-- nodes_path_key — автогенерированное Postgres имя для UNIQUE на одной колонке.
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_path_key;
ALTER TABLE nodes
    ADD CONSTRAINT nodes_team_path_unique UNIQUE (team_id, path);

-- 4. users.team_id (VARCHAR) → users.default_team_id (UUID FK).
ALTER TABLE users ADD COLUMN default_team_uuid UUID;
UPDATE users u
   SET default_team_uuid = (SELECT id FROM teams t WHERE t.slug = u.team_id);
UPDATE users
   SET default_team_uuid = (SELECT id FROM teams WHERE slug = 'default')
 WHERE default_team_uuid IS NULL;
ALTER TABLE users ALTER COLUMN default_team_uuid SET NOT NULL;
ALTER TABLE users
    ADD CONSTRAINT users_default_team_id_fkey
    FOREIGN KEY (default_team_uuid) REFERENCES teams(id) ON DELETE RESTRICT;
ALTER TABLE users DROP COLUMN team_id;
ALTER TABLE users RENAME COLUMN default_team_uuid TO default_team_id;
CREATE INDEX users_default_team_id_idx ON users (default_team_id);

-- 5. Membership many-to-many.
CREATE TABLE user_teams (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id    UUID        NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    role       VARCHAR(16) NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, team_id),
    CONSTRAINT user_teams_role_check CHECK (role IN ('owner', 'admin', 'member'))
);
CREATE INDEX user_teams_team_id_idx ON user_teams (team_id);

-- 'admin' юзер становится owner'ом default-team.
INSERT INTO user_teams (user_id, team_id, role)
SELECT u.id, t.id, 'owner'
  FROM users u
  CROSS JOIN teams t
 WHERE u.login = 'admin' AND t.slug = 'default'
ON CONFLICT DO NOTHING;

-- 6. api_tokens — токен привязан к команде, не к юзеру.
ALTER TABLE api_tokens ADD COLUMN team_id UUID;
UPDATE api_tokens at
   SET team_id = (SELECT id FROM teams WHERE slug = 'default');
ALTER TABLE api_tokens ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE api_tokens
    ADD CONSTRAINT api_tokens_team_id_fkey
    FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE;
CREATE INDEX api_tokens_team_id_idx ON api_tokens (team_id);

-- 7. user_audit — контекст события (NULL = глобальное действие админа).
ALTER TABLE user_audit ADD COLUMN team_id UUID;
ALTER TABLE user_audit
    ADD CONSTRAINT user_audit_team_id_fkey
    FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE SET NULL;
CREATE INDEX user_audit_team_id_idx ON user_audit (team_id);
