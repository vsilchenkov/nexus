-- 0024: максимум per-node timeout_ms поднят с 300000 до 600000 мс (10 минут).
-- Синхронно с domain.Node.Validate (internal/domain/node.go) и UI-валидацией.
ALTER TABLE nodes DROP CONSTRAINT nodes_timeout_range;
ALTER TABLE nodes ADD CONSTRAINT nodes_timeout_range CHECK (timeout_ms BETWEEN 100 AND 600000);
