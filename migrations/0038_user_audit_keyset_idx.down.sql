-- Откат §91.1: удаляем индексы keyset-пагинации. Данные не затрагиваются —
-- журнал продолжит работать на user_audit_created_at_idx, просто медленнее на
-- больших объёмах.
DROP INDEX IF EXISTS user_audit_global_created_id_idx;
DROP INDEX IF EXISTS user_audit_team_created_id_idx;
