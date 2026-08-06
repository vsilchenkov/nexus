-- 0033_node_circuit_breaker.down.sql
--
-- Откат теряет per-node настройки circuit breaker'а (§81.3): узлы возвращаются
-- к глобальной политике из конфигурации. Данных доставки это не касается.
ALTER TABLE nodes DROP COLUMN IF EXISTS circuit_breaker_cooldown_sec;
ALTER TABLE nodes DROP COLUMN IF EXISTS circuit_breaker_threshold;
