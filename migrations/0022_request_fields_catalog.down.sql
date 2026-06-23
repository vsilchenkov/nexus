-- 0022_request_fields_catalog.down.sql
--
-- Откат справочника полей запроса (§41). Имена в nodes.auth_dynamic_field /
-- incoming_auth_dynamic_field — денормализованные копии, не FK, поэтому удаление
-- каталога безопасно (узлы продолжают работать со своими значениями).

DROP TABLE IF EXISTS request_fields_catalog;
