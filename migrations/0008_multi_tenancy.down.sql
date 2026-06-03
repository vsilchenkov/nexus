-- 0008_multi_tenancy.down.sql
--
-- Откат Phase A.1. Возвращаем team_id как VARCHAR('default').
-- Сценарий: только если кто-то не успел уйти от 'default' team и
-- никаких других команд не появилось — иначе данные будут потеряны.

-- 1. user_audit.team_id
DROP INDEX IF EXISTS user_audit_team_id_idx;
ALTER TABLE user_audit DROP CONSTRAINT IF EXISTS user_audit_team_id_fkey;
ALTER TABLE user_audit DROP COLUMN IF EXISTS team_id;

-- 2. api_tokens.team_id
DROP INDEX IF EXISTS api_tokens_team_id_idx;
ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_team_id_fkey;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS team_id;

-- 3. user_teams
DROP TABLE IF EXISTS user_teams;

-- 4. users.default_team_id → team_id VARCHAR
ALTER TABLE users ADD COLUMN team_id_str VARCHAR(64);
UPDATE users u
   SET team_id_str = (SELECT slug FROM teams t WHERE t.id = u.default_team_id);
UPDATE users SET team_id_str = 'default' WHERE team_id_str IS NULL;
ALTER TABLE users ALTER COLUMN team_id_str SET NOT NULL;
ALTER TABLE users ALTER COLUMN team_id_str SET DEFAULT 'default';
DROP INDEX IF EXISTS users_default_team_id_idx;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_default_team_id_fkey;
ALTER TABLE users DROP COLUMN default_team_id;
ALTER TABLE users RENAME COLUMN team_id_str TO team_id;

-- 5. UNIQUE(team_id, path) → UNIQUE(path)
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_team_path_unique;
ALTER TABLE nodes ADD CONSTRAINT nodes_path_key UNIQUE (path);

-- 6. nodes.team_id (UUID) → team_id (VARCHAR)
ALTER TABLE nodes ADD COLUMN team_id_str VARCHAR(64);
UPDATE nodes n
   SET team_id_str = (SELECT slug FROM teams t WHERE t.id = n.team_id);
UPDATE nodes SET team_id_str = 'default' WHERE team_id_str IS NULL;
ALTER TABLE nodes ALTER COLUMN team_id_str SET NOT NULL;
ALTER TABLE nodes ALTER COLUMN team_id_str SET DEFAULT 'default';
DROP INDEX IF EXISTS nodes_team_id_idx;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_team_id_fkey;
ALTER TABLE nodes DROP COLUMN team_id;
ALTER TABLE nodes RENAME COLUMN team_id_str TO team_id;
CREATE INDEX nodes_team_id_idx ON nodes (team_id);

-- 7. teams
DROP TABLE IF EXISTS teams;
