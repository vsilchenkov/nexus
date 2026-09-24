-- 0043_node_sse_breaker_timeout.down.sql
--
-- Откат §107.
--
-- ПОТЕРЯ ДАННЫХ: удаляются оба флага узлов. После возврата на прежнюю версию
-- кода узлы снова пропускают GET-подписку SSE к получателю (она висит до
-- таймаута) и засчитывают таймауты breaker'у — ровно поведение до §107. При
-- повторном накате флаги придётся включить заново на тех же узлах (MCP-узлы
-- команды mcp). rollback_info.py пометит потерю сам (DROP COLUMN).

ALTER TABLE nodes DROP COLUMN IF EXISTS breaker_ignore_timeouts;

ALTER TABLE nodes DROP COLUMN IF EXISTS sse_decline;
