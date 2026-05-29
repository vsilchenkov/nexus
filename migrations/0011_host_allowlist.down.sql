-- Откат 0011: в обратном порядке (trigger → function → M2M → каталог).
DROP TRIGGER IF EXISTS node_allowed_hosts_usage_trg ON node_allowed_hosts;
DROP FUNCTION IF EXISTS host_allowlist_bump_usage();
DROP TABLE IF EXISTS node_allowed_hosts;
DROP TABLE IF EXISTS host_allowlist;
