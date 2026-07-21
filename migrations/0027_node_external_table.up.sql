-- 0027_node_external_table.up.sql
--
-- §64 (внешняя/ручная таблица логов): признак того, что CH-таблицей узла
-- управляет не Nexus, а сам оператор или посторонний сервис-писатель.
--
-- При external_table = true Nexus не создаёт и не переименовывает таблицу, не
-- применяет стартовые ALTER'ы миграции схемы (node_id / http_method /
-- request_size / response_size + backfill), не дропает партиции по retention и
-- запрещает §56 schema-sync. Читать логи и считать метрики из такой таблицы
-- по-прежнему можно — это и есть сценарий «Nexus как фронт логирования».
ALTER TABLE nodes ADD COLUMN external_table BOOLEAN NOT NULL DEFAULT false;

-- Legacy-узлы с ручной таблицей (clickhouse_template_id IS NULL, §19) до этой
-- миграции обслуживались Nexus'ом как обычные: провижининг по дефолтному
-- шаблону, стартовые ALTER'ы, housekeeping. Проставляем им дефолтный шаблон,
-- чтобы поведение не изменилось молча — иначе их retention просто перестал бы
-- работать и таблицы росли бы бесконечно. «Ручными» остаются только те узлы,
-- где оператор явно выберет «(нет / ручная таблица)» уже после обновления.
--
-- Если дефолтного шаблона в ch_templates нет, UPDATE не выполняется: узлы
-- остаются с NULL и сохраняют legacy-поведение (провижининг по GetDefault при
-- следующем сохранении узла).
UPDATE nodes
SET clickhouse_template_id = (SELECT id FROM ch_templates WHERE is_default LIMIT 1)
WHERE clickhouse_template_id IS NULL
  AND clickhouse_table <> ''
  AND EXISTS (SELECT 1 FROM ch_templates WHERE is_default);
