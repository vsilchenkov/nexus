# 67. Хост клиента в логах (reverse-DNS) + фильтр + счётчик «Всего»

Раздел добавляет в CH-логи узлов колонку **`client_host`** — PTR-имя (reverse DNS) IP
клиента-отправителя (например, `SRV-P-1C-NODE3.vz78.vozovoz.ru` для `192.168.86.246`), фильтр
«Хост клиента» в расширенных фильтрах логов и счётчик «Показано N из M» в шапке. Отвечает на
вопрос «кто ходил через узел»: раньше был виден только IP (`IP`), а колонка `Host` хранит
hostname самого Sender'а (docker container ID). Развивает §4.3 (схема логов), §42.10 (ensure
колонок), §48 (фильтры логов), §64 (внешние таблицы).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 67.1 Колонка `client_host`

- `client_host String DEFAULT ''`, физически **сразу после `IP`** — в `RequiredLogColumns`
  ([domain/ch_log_schema.go](../../internal/domain/ch_log_schema.go)), откуда автоматически:
  CREATE новых таблиц (RenderCreateTable), verify внешних таблиц (§64), whitelist CODEC/index.
- Существующие управляемые таблицы — стартовый ensure §42.10 в Web и Sender:
  `ALTER TABLE <t> ADD COLUMN IF NOT EXISTS client_host String DEFAULT '' AFTER IP`
  (`EnsureClientHostColumn`). Внешние таблицы (§64) в списки ensure не попадают.
- Пустое значение: имя ещё не отрезолвлено (холодный кеш), PTR-записи нет, `ClientIP` — не IP
  (`rabbitmq://…` у pull-узлов §27.10) или legacy-строка до §67.
- `domain.LogRecord.ClientHost`; заполняется в Sender: `SendUsecase.Send` (sync + async) и
  `DLQReprocessor.logTTLExpired`. Kafka-retry логов (§38) прокидывает поле через clogwire
  автоматически; сообщения старого Sender'а дают `""`.

## 67.2 Резолвер (`internal/platform/rdns`)

Порт `HostResolver { Lookup(ip string) string }` объявлен в `sender/usecase` (консьюмер-сайд,
как `CircuitBreaker`), подключается functional option'ом `WithHostResolver`.

**Контракт `Lookup` — ноль I/O на пути доставки.** Значение берётся только из L1-кеша в памяти;
промах немедленно возвращает `""` и планирует фоновый резолв — имя получат следующие записи
этого IP. Путь запроса никогда не ждёт ни DNS, ни Redis.

Фоновый резолв (safego-горутина, дедуп pending-набором + singleflight):

1. Redis `GET nexus:rdns:<ip>` — общий кеш реплик, переживает рестарты. Пустая строка —
   валидный негативный хит (промах отличается по redis.Nil).
2. Промах → `net.DefaultResolver.LookupAddr` с таймаутом `timeout_ms`; PTR-имя обрезается от
   финальной точки.
3. Redis `SET EX`: позитив — `cache_ttl_sec`, негатив (нет PTR/ошибка DNS) — `negative_ttl_sec`.
   Плюс запись в L1 с TTL = min(TTL, 5 мин) — потолок, чтобы реплика подхватывала обновления
   соседей не позже чем через 5 минут.

**Fail-open всюду**: Redis недоступен/nil → режим «только L1» с Debug-логом; ошибка DNS →
негативный кеш. Debug-инструментация §51.9: ip, host, source (redis|dns|dns_negative),
duration_ms.

**Сознательные отказы** (зафиксированы, чтобы не переизобретать):
- Кросс-репличного дедупа резолвов нет: Redis-лок сложнее самой фичи; худший случай — по
  одному PTR-запросу на IP с каждой реплики раз в TTL.
- Prometheus-метрик нет: объём работы виден по Debug-логам (уровень включается на лету, §51).

Конфиг `sender.rdns` (во всех трёх config-файлах): `disabled` (default false — включён),
`timeout_ms` (2000), `cache_ttl_sec` (3600), `negative_ttl_sec` (600).

Требование к инфраструктуре: PTR-записи в корпоративном DNS для клиентских подсетей; резолв
должен работать из контейнера Sender'а (`docker exec <sender> getent hosts <ip>`).

## 67.3 UI: фильтр «Хост клиента» + facet

- `GET /api/nodes/{id}/logs/client-hosts` — DISTINCT непустых `client_host` узла (кап 200,
  сортировка), деградации как у `/logs/methods` (`logs_configured`/`logs_available`).
- `LogQuery.ClientHost` — exact match: SQL-условие в Search/Count + in-memory зеркало
  live-tail (`matchLogFilter`). Query-параметр `client_host` в `GET /logs`, `/logs/stream`,
  `/logs/count`.
- UI (`LogClientHostFilter.tsx`, копия `LogMethodFilter`): cmdk-комбобокс с загрузкой при
  открытии, пункт «Любой». Размещение (эскиз утверждён): второй ряд расширенных фильтров —
  «Дата с», «Дата по» (метки сверху, ширина по контенту), «Хост клиента», кнопки
  «Сбросить»/«Применить» в том же ряду. **Колонки в таблице логов НЕТ** (сознательно);
  `ClientHost` в JSON записи лога (`LogRecordDTO`) тоже не отдаётся — добавляется одной
  строкой, когда понадобится.

## 67.4 Счётчик «Показано N из M»

- `GET /api/nodes/{id}/logs/count` → `{total, logs_configured, logs_available}` — точный
  `count()` под **теми же** фильтрами, что и список: общий построитель `searchConds` у Search
  и Count (единственный источник WHERE). `before_id` игнорируется — total по фильтрам, не по
  странице. Плохой `q` → 400.
- Стоимость count() и меры деградации:

| Фильтры | Стоимость | Мера |
|---|---|---|
| даты (+node_id) | дёшево (partition/ORDER BY pruning) | — |
| + method/client_host/status/done/IP | умеренно (лёгкие колонки, тела не читаются) | — |
| + полнотекст `q` по телам | дорого (LIKE-скан request/response) | серверный таймаут 10с (`countTimeout`) → `ErrLogsBackendUnavailable` → 200 + `logs_available=false` |

- UI: отдельный react-query запрос (таблица не ждёт count, «из M» дорисовывается); queryKey =
  параметры списка + быстрые фильтры ok/err и done/pending (они тоже входят в total);
  пересчёт со снапшотом, НЕ на SSE-события; в Live выключен и счётчик скрыт (total мгновенно
  устаревает); count недоступен → прежнее «Показано N» без «из».

## 67.5 Внешние таблицы (§64) и деплой-окно

Nexus не ALTER'ит внешние таблицы — колонку добавляет владелец вручную (SQL обязан попасть в
описание релиза, см. правило в DEPLOYMENT.md §9.5):

```sql
ALTER TABLE <db>.<table> ADD COLUMN IF NOT EXISTS client_host String DEFAULT '' AFTER IP
```

До выполнения ALTER (деплой-окно) — везде мягкая деградация, без 500/Sentry-флуда:

- **Запись**: INSERT Sender'а по такой таблице падает → записи буферизуются в Kafka-retry
  (§38) и доигрываются после ALTER. Потери нет.
- **Чтение (список/детали/live/count с фильтром)**: SELECT'ы читают `client_host` списком и
  падают на CH — `classifyCHErr` распознаёт отсутствие ИМЕННО этой колонки
  (`isMissingColumnErr`, CH-коды 10/16/47) и помечает ошибку `ErrLogsBackendUnavailable` →
  UI показывает штатный баннер «Логи временно недоступны» (200 + `logs_available=false`).
  Без этого поллинг вкладки «Логи» флудил бы 500/Sentry раз в несколько секунд до ALTER
  (поймано на стенде). Отсутствие ДРУГИХ колонок остаётся серверной ошибкой — реальные
  поломки DDL не маскируются. `count()` без фильтра по хосту колонку не читает и работает.
- **Facet `/logs/client-hosts`**: пустой список + Debug (тот же `isMissingColumnErr`).
- **Verify** (`POST /api/ch-tables/verify`): показывает `Missing: ["client_host"]` — сигнал
  владельцу выполнить ALTER. Проверено на стенде живьём: до ALTER — баннер и пустой facet,
  после ALTER — чтение и счётчик восстанавливаются без рестартов.
