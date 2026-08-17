-- §91.1: индекс под keyset-пагинацию журнала аудита.
--
-- Список читается как `ORDER BY created_at DESC, id DESC` с курсором
-- `(created_at, id) < ($ts, $id)`. Существующий user_audit_created_at_idx
-- покрывает только первый компонент: на равных метках времени PostgreSQL
-- досортировывает страницу, а курсор по кортежу индексом не поддержан.
--
-- Аддитивная миграция: старый индекс не трогаем (его используют выборки по
-- периоду и housekeeping), данные не меняются.
CREATE INDEX IF NOT EXISTS user_audit_created_at_id_idx
    ON user_audit (created_at DESC, id DESC);
