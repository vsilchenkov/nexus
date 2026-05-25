-- 0006_app_settings.up.sql
--
-- §8.4 / §14.5 ТЗ: динамическая часть настроек Sentry и ClickHouse —
-- хранится в БД и накладывается поверх YAML/env при старте; меняется
-- через UI без рестарта (hot-reload через Redis pub/sub — Phase 6.3.2).
--
-- Singleton: ровно одна строка (id = 1), значения в одном JSONB-поле.
-- Так атомарный update проще, и не нужно держать ENUM ключей в коде.

CREATE TABLE app_settings (
    id          INTEGER     PRIMARY KEY DEFAULT 1,
    value       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  UUID        REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT app_settings_singleton CHECK (id = 1)
);

-- Заводим строку сразу — usecase читает её без проверки на NoRows.
INSERT INTO app_settings (id, value) VALUES (1, '{}'::jsonb);
