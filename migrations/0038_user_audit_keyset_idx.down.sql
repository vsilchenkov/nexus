-- Откат §91.1: удаляем индекс keyset-пагинации. Данные не затрагиваются —
-- список аудита продолжит работать на user_audit_created_at_idx, просто
-- медленнее на больших объёмах.
DROP INDEX IF EXISTS user_audit_created_at_id_idx;
