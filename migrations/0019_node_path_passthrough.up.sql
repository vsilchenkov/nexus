-- 0019_node_path_passthrough.up.sql
--
-- §39 ТЗ: path-passthrough маршрутизация. Когда флаг включён, хвост входящего
-- пути после пути узла приклеивается к Target URL узла (прозрачное
-- проксирование). Например узел path='ozon', запрос
-- /api/v1/request/<team>/ozon/GetAuthToken → внешний вызов на
-- <target_url>/GetAuthToken. По умолчанию ВЫКЛ — точный матч пути сохраняется
-- (обратная совместимость со всеми существующими узлами).
--
-- NOT NULL DEFAULT false атомарно заполняет все существующие узлы — отдельный
-- backfill не нужен.

ALTER TABLE nodes
    ADD COLUMN path_passthrough BOOLEAN NOT NULL DEFAULT false;
