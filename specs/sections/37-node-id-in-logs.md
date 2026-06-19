## 37. Идентификатор узла в логах (per-node атрибуция в общих ClickHouse-таблицах)

Несколько узлов могут писать в **одну** ClickHouse-таблицу логов (поле `clickhouse_table` узла
задаётся оператором; типичный случай — sync- и async-варианты одного интеграционного эндпоинта,
напр. `webhook/send` и `webhook/sendasynq` → одна `webhook_send`). До §37 запись лога
(`domain.LogRecord`) не содержала идентификатора узла, поэтому все per-node запросы на общей таблице
**смешивали записи разных узлов**: счётчики/графики рабочего стола и страницы узла совпадали для
разных узлов, а очистка/replay «неудачных» одного узла задевали записи другого (включая опасный
`DELETE`). §37 добавляет колонку `node_id` и фильтрацию по ней во всех per-node чтениях и удалениях.

### 37.1 Модель данных

В обязательную схему лог-таблицы (`RequiredLogColumns`, §4.3) добавляется колонка **`node_id String`**
(в конец, после `attempts_details`; порядок INSERT/SELECT — инвариант). Значение — **UUID узла**
(`domain.Node.ID`), неизменный при переименовании/переносе узла (в отличие от пути). `domain.LogRecord`
получает поле `NodeID string`.

### 37.2 Запись (Sender)

`node.ID` прокидывается во все точки записи лога:

- **sync** (`/v1/request`): Receiver кладёт `node.ID` в gRPC `SendRequest.node_id` (поле 28) →
  `sender_service` → `SendInput.NodeID` → `SendUsecase.Send` пишет `rec.NodeID`.
- **async** (`/v1/requestAsync`) и **DLQ-retry**: `buildSendInput(node, env)` ставит `NodeID = node.ID`
  (узел резолвится локально в Sender по `env.NodePath`).
- **DLQ TTL-drop**: `logTTLExpired` ставит `NodeID = node.ID`.
- File-fallback (NDJSON) сериализует `LogRecord` целиком — поле подхватывается автоматически.

### 37.3 Миграция существующих таблиц

Колонка добавляется в шаблон → **новые** таблицы получают её при создании (`RenderCreateTable`). Для
**существующих** таблиц на старте **и Web, и Sender** (порядок деплоя не гарантирован, а без колонки
INSERT/SELECT по новой схеме упадут) выполняется идемпотентный
`ALTER TABLE <t> ADD COLUMN IF NOT EXISTS node_id String DEFAULT ''` — общий хелпер
`platform/clickhouse.EnsureNodeIDColumn`. Дешёвая метаданные-операция, concurrent-safe; список таблиц:
Web — из `nodeRepo.List` (default-team), Sender — `nodepg.ListClickHouseTables` (все узлы с таблицей).
Ошибка по одной таблице — log+continue.

### 37.4 Чтение и удаление (Web)

Все per-node запросы `LogReaderCH` фильтруют **`(node_id = ? OR node_id = '')`**: `ListSince`
(live-tail), `Search`, `CountErrors`, `CountFailed`, `FailedIDs`, `DeleteFailed`, `NodeKPI`, `NodeChart`.
`GetByID` не фильтрует (читает по уникальному `ID`). `nodeID` прокидывается из usecase-слоя (узел уже
резолвлен — `n.ID`): `LogsUsecase`, `MetricsUsecase` (per-node throughput рабочего стола и страница
узла), `ReplayUsecase.ReplayFailed`, `AsyncQueueUsecase.PurgeFailed`. Глобальные KPI рабочего стола и
Kafka-мониторинг — из Prometheus, не затрагиваются.

**Legacy-семантика (`OR node_id = ''`).** Записи до миграции имеют `node_id = ''`. Они трактуются как
принадлежащие любому co-table узлу: на **не-общей** таблице (один узел) это просто «все записи узла»
(корректно); на **общей** — старые записи временно видны/чистятся у всех co-table узлов (неразличимы —
принятый компромисс), а **новый** трафик строго per-node. `''`-записи истекают по TTL таблицы.

### 37.5 UI

Во вкладке «Конфиг» узла выводится его **node id** (UUID, read-only, копируемый) — оператор видит, какой
идентификатор соответствует узлу (полезно при общих таблицах). API уже отдаёт `id` в node DTO.

### 37.6 Scope и неочевидности

**В scope:** колонка `node_id`; запись во всех путях (вкл. gRPC proto-поле); миграция существующих
таблиц в обоих сервисах; фильтр во всех per-node чтениях/удалениях; node id в UI.

- **Почему `node_id` (UUID), а не `node_path`:** путь меняется при переименовании/переносе узла, UUID —
  нет; атрибуция сохраняется. Цена — проброс `node.ID` через sync gRPC (async/DLQ резолвят узел локально).
- **Почему `OR node_id = ''`:** обратная совместимость со старыми записями без миграции их значений
  (`ALTER … UPDATE` на больших таблицах дорог). Старые записи неразличимы между co-table узлами, но это
  только historical; новый трафик чист.
- **Почему миграция в обоих сервисах:** Sender пишет, Web читает; обоим нужна колонка до первой
  операции; порядок деплоя не контролируем → оба идемпотентно ensure-ят на старте.
- **`node_id` не в `ORDER BY`** (он в шаблоне, `ALTER ORDER BY` невозможен) → фильтр = доп. условие
  внутри time-партиции (скан, не индекс). Для time-bounded запросов приемлемо; при необходимости — опц.
  `INDEX node_id TYPE bloom_filter` (вне §37).

**Out of scope:** бэкфилл `node_id` старых записей; вторичный индекс по `node_id`; запрет общих таблиц.
