-- §91.1: индексы под keyset-пагинацию журнала аудита.
--
-- Список читается как `WHERE team_id = $1 [OR team_id IS NULL]
-- ORDER BY created_at DESC, id DESC` с курсором `(created_at, id) < ($ts, $id)`.
-- Существующий user_audit_created_at_idx покрывает только сортировку и ничего
-- не знает ни о команде, ни о втором компоненте курсора.
--
-- Два индекса, потому что запрос ходит двумя разными путями:
--   * командный (основной, любая роль) — ведущая колонка team_id, дальше
--     порядок сортировки: страница «редкой» команды не просматривает чужие
--     строки;
--   * глобальные записи (team_id IS NULL, admin — §91.2) в первый индекс не
--     попадают: NULL там есть, но частичный индекс компактнее и обслуживает
--     ветку `OR team_id IS NULL`, которую планировщик берёт отдельным узлом.
-- Оба покрывают и `count(*)` счётчика «показано N из M» — он идёт по тем же
-- условиям, что и список.
--
-- Аддитивная миграция: старый индекс не трогаем (его используют выборки по
-- периоду и housekeeping по created_at), данные не меняются.
CREATE INDEX IF NOT EXISTS user_audit_team_created_id_idx
    ON user_audit (team_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS user_audit_global_created_id_idx
    ON user_audit (created_at DESC, id DESC)
    WHERE team_id IS NULL;
