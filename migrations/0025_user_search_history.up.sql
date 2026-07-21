-- 0025_user_search_history.up.sql
--
-- §62 (глобальный поиск узлов): персональная история поисковых запросов
-- пользователя (последние 10 значений), общая для глобального поиска в шапке
-- и поля «Поиск» на странице узлов. Строго per-user, не пересекается между
-- пользователями.
--
-- PK (user_id, query) даёт дедуп по строке: повторный поиск той же строки —
-- ON CONFLICT DO UPDATE SET searched_at = now() (запись всплывает наверх).
-- Прямой FK на users(id) ON DELETE CASCADE: история привязана к пользователю,
-- не к команде (запросы кросс-командные), удаление пользователя чистит историю.
--
-- Обрезка до 10 самых свежих записей — на стороне приложения (DELETE в той же
-- транзакции, что и upsert). Индекс (user_id, searched_at DESC) обслуживает и
-- чтение списка, и подзапрос обрезки.
CREATE TABLE user_search_history (
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    query       TEXT        NOT NULL,
    searched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, query),
    CONSTRAINT user_search_history_query_len CHECK (char_length(query) BETWEEN 1 AND 200)
);

CREATE INDEX user_search_history_recent_idx ON user_search_history (user_id, searched_at DESC);
