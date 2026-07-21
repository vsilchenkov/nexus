-- 0026_node_author.up.sql
--
-- §63 (автор изменения узла): логин того, кто создал и кто последним изменил
-- узел — для показа рядом с «Создано»/«Обновлено» в инфо-вкладке узла.
--
-- Две колонки-логина (VARCHAR, как node_allowed_hosts.created_by / headers_catalog
-- / request_fields_catalog): логин — не секрет, джойн к users не нужен, рендерится
-- напрямую. created_by пишется при создании и дальше не меняется; updated_by —
-- при каждом изменении узла (в тех же UPDATE, что бампают updated_at). Аддитивно:
-- у существующих узлов пусто (в UI «Автор: —»), заполнится при следующей правке.
ALTER TABLE nodes ADD COLUMN created_by VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN updated_by VARCHAR(255) NOT NULL DEFAULT '';
