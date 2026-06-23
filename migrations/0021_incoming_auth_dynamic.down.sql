-- 0021_incoming_auth_dynamic.down.sql
--
-- Откат входящей динамической авторизации (§41): убираем constraints и колонки.
-- Дефолты header/Authorization означают, что данные не теряются по смыслу —
-- после отката basic/token снова читают только заголовок Authorization.

ALTER TABLE nodes DROP CONSTRAINT nodes_inc_auth_dyn_fld_len;
ALTER TABLE nodes DROP CONSTRAINT nodes_inc_auth_dyn_fld_fmt;
ALTER TABLE nodes DROP CONSTRAINT nodes_inc_auth_dyn_src_check;

ALTER TABLE nodes DROP COLUMN incoming_auth_dynamic_field;
ALTER TABLE nodes DROP COLUMN incoming_auth_dynamic_source;
