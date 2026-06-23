-- 0021_incoming_auth_dynamic.up.sql
--
-- §41 ТЗ: универсальная динамическая авторизация. Входящая авторизация
-- (incoming_auth_type = 'basic' / 'token') теперь может читать креду клиента не
-- только из жёстко заданного заголовка Authorization, но из произвольного
-- источника — заголовка ИЛИ query-параметра — по настраиваемому имени поля.
--
-- Две новые колонки (симметрично исходящим auth_dynamic_source/field):
--   incoming_auth_dynamic_source — 'header' (дефолт, прежнее поведение) или 'query';
--   incoming_auth_dynamic_field  — имя заголовка/параметра ('Authorization' по
--                                  умолчанию → поведение существующих узлов не
--                                  меняется; для старого кэш-JSON без поля
--                                  zero-value трактуется как header/Authorization).
-- 'body' для входящей валидации не предусмотрен (нет гейт-кейса).
-- Аддитивно: дефолты сохраняют поведение всех существующих basic/token-узлов.

ALTER TABLE nodes
    ADD COLUMN incoming_auth_dynamic_source VARCHAR(16) NOT NULL DEFAULT 'header',
    ADD COLUMN incoming_auth_dynamic_field  VARCHAR(64) NOT NULL DEFAULT 'Authorization';

ALTER TABLE nodes ADD CONSTRAINT nodes_inc_auth_dyn_src_check
    CHECK (incoming_auth_dynamic_source IN ('header', 'query'));
-- Имя поля — латиница на старте, далее буквы/цифры/дефис/подчёркивание (то же
-- ограничение, что и у исходящего auth_dynamic_field; покрывает имена заголовков
-- вроде X-Api-Key и query-параметров).
ALTER TABLE nodes ADD CONSTRAINT nodes_inc_auth_dyn_fld_fmt
    CHECK (incoming_auth_dynamic_field ~ '^[a-zA-Z][a-zA-Z0-9_-]*$');
ALTER TABLE nodes ADD CONSTRAINT nodes_inc_auth_dyn_fld_len
    CHECK (char_length(incoming_auth_dynamic_field) BETWEEN 1 AND 64);
