## 35. Переработка вкладки «Очередь» (производительность, источник из логов, честная семантика)

Этот раздел **перерабатывает** подход к управлению async-очередью узла requestAsync из §34.4/§34.6.
На живом стенде прежняя реализация оказалась неработоспособной в реальном (самом частом) сценарии:
узел `enabled`, внешний адрес недоступен. Раздел исправляет источник данных, стоимость, семантику
очистки, интерфейс и RBAC.

### 35.1. Корень проблемы — модель потока

Прежний план исходил из «адрес недоступен → данные копятся в очереди `nexus.async`». По факту (см.
`internal/sender/usecase/async.go` `Handle()`):

| Статус узла | Адрес жив | Адрес мёртв |
|---|---|---|
| **enabled** | доставлено → 2xx → Ack (в очереди пусто) | доставка падает → **DLQ** `nexus.async.dlq` + **коммит** (в `nexus.async` пусто, LAG=0) |
| **paused** | копится в `nexus.async` (HandleRetry, без коммита) | копится в `nexus.async` |
| **disabled** | Ack-дроп | Ack-дроп |

То есть **живая очередь `nexus.async` наполняется только для paused-узла** (или когда лежит Sender). Для
enabled-узла с мёртвым адресом всё уходит в DLQ. Прошлый стенд-тест ставил узел на паузу — и тем
замаскировал реальный поток.

### 35.2. Источник «неудачных доставок» — ClickHouse, не Kafka-DLQ-peek

`send.Send()` (который **всегда** пишет лог при `LoggingEnabled`) вызывается **до** `publishDLQ`. Значит
на каждое ушедшее в DLQ сообщение есть запись в ClickHouse с `done=false` (status, reason, тело при
`log_request_body`, attempts, время). ClickHouse быстр (индексы по `done`/`date_request`), уже есть
фильтр `done=no`, ленивое тело `GET /api/nodes/{id}/log/{logId}` и **replay**.

**Решение:** «неудачные доставки» во вкладке читаются из ClickHouse-логов (`done=false`), а **дорогой
Kafka-DLQ-peek удаляется** (§34.6 `PeekDLQDepth/PeekDLQList` сканировали до 5000 сообщений × 4 партиции,
фронт дёргал 4 эндпоинта каждые 5с — при 248k DLQ это катастрофа). Для KPI добавляется дешёвый счётчик
`LogReader.CountFailed` (`SELECT count() ... WHERE done=0 AND <окно>`) и endpoint
`GET /api/nodes/{id}/logs/failed-count?from=&to=` (scope `logs:read`). Список/тело/replay — существующие
`GET /api/nodes/{id}/logs?done=no` + `/log/{logId}` + `POST /api/logs/{id}/replay`.

### 35.3. Семантика очистки (честно)

- **Живая очередь** (`nexus.async`, paused-узлы) — управляется tombstone'ами (§34.4): delete одного /
  purge за период / всё. Остаётся, **admin-only**. В UI кнопки видны только когда есть pending (не no-op).
- **Неудачные доставки** (DLQ-сценарий) — **не удаляются** из вкладки. Причины: Kafka-DLQ физически не
  чистится (нет `DeleteRecords` в `segmentio/kafka-go`, партиция общая для узлов), а CH-логи — это история
  (истекает по log-retention/housekeeping). Вместо фейковой кнопки: фильтр по периоду (видны свежие),
  **«Пауза»/«Отключить» узел** (остановить рост) и **replay** (переотправить). DLQ истекает по
  `retention.ms` (30 дней).

### 35.4. Лёгкая смена статуса узла

Сейчас статус меняется только полным `PUT /api/nodes/{id}` (перезапись всех полей). Для кнопок
«Пауза»/«Отключить» вводится лёгкий `PATCH /api/nodes/{id}/status {status}` (роль **manager+**, как
Update): валидирует `status ∈ {enabled,paused,disabled}`, меняет только статус (audit-дифф), не трогая
прочие поля и креды. Полезен и для будущего quick-toggle на Overview.

### 35.5. RBAC-фикс (баг из §34.4)

Прежде вкладка «Очередь» показывалась всем ролям, а async-queue-эндпоинты были admin-only → manager/
viewer получали 403. После §35:

- **Failed-view** (счётчик/список/тело/replay из логов) — `logs:read` (viewer+).
- **Управление живой очередью** (peek live + tombstone delete/purge) — **admin**.
- Фронт: вкладка видна viewer+ (failed-view работает); секция «Ожидают отправки» (admin-операции)
  скрыта для не-admin (`useRoleAtLeast("admin")`); кнопки «Пауза/Отключить» — manager+.

### 35.6. Интерфейс

Вместо шапки «В ОЧЕРЕДИ СЕЙЧАС 0 / Очистить всё»:

```
[ Заголовок + PeriodPicker ]
[ KpiRow:  «Ожидают отправки» (живая очередь, admin)  |  «Неудачные доставки» (CH done=0 за период) ]
[ Hint-баннер: пояснение + «Пауза»/«Отключить» (manager+) + про replay/retention ]
[ Секция «Ожидают отправки» — только admin И (pending>0 ИЛИ paused): таблица + delete + purge ]
[ Секция «Неудачные доставки» — всегда (если есть clickhouse_table): таблица (время/метод/url/status/
  reason) + ленивое тело + replay + «Открыть в логах» (дип-линк на Logs с done=no + период) ]
```

Состояния: загрузка / пусто / логирование выключено (`logs_configured:false`) / нет-прав. Переиспользуются
типы и компоненты логов (`LogRow/LogDetail`, `LogBodies`, `ReplayDialog`, `LogsInitialFilter` +
новое поле `done`), UI-kit (`Kpi/KpiRow/Hint/Pill/PeriodPicker`).

### 35.7. Scope / Out of scope

**В scope:** удаление Kafka-DLQ-peek; `CountFailed` + endpoint; failed-view из CH-логов (список/тело/
replay/дип-линк); `PATCH /nodes/:id/status`; RBAC-фикс; редизайн вкладки; удешевление живой очереди
(cap, без отдельного `/depth`); тесты; бандл; i18n.

**Out of scope:** физическое удаление сообщений из Kafka-DLQ (невозможно — нет `DeleteRecords`, общая
партиция); глобальное снижение `retention.ms` DLQ через AlterConfigs (кластерное, не per-node — при
необходимости на экране Kafka-мониторинга, не здесь); удаление CH-записей неудач (разрушает историю —
выбран вариант «без удаления»).
