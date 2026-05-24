-- 0001_init_schema.up.sql
--
-- Пустая инициализация — нужна только для того, чтобы golang-migrate
-- создал служебную таблицу schema_migrations и зарегистрировал версию 1.
-- Реальные доменные таблицы (nodes, users, methods, …) появляются
-- в миграции 0002+ в Phase 1.

-- Включаем расширение для UUID v4 на будущее (Phase 1 будет использовать).
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
