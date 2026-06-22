## 39. Path-passthrough — приклеивание хвоста пути к Target URL + лог-колонки http_method/method

До §39 Receiver матчил узел по **точному** пути (`node_path`) и в режиме `static` отправлял запрос
ровно на `target_url`, не учитывая «хвост» пути после имени узла. То есть узел `ozon` принимал только
`…/request/<team>/ozon`, а `…/request/<team>/ozon/GetAuthToken` давал `404 node not found`. Nexus вёл
себя не как прозрачный прокси: один узел = один эндпоинт. §39 добавляет **опциональный**
path-passthrough (хвост входящего пути приклеивается к `target_url`) и разводит в логах HTTP-глагол и
вызываемый подпуть по двум колонкам.

### 39.1 Модель данных

Новое поле узла **`path_passthrough BOOLEAN NOT NULL DEFAULT false`** (миграция `0019`). По умолчанию
выключено — точный матч пути и `404` на лишний хвост сохраняются для всех существующих узлов (нулевая
смена поведения). Поле живёт в `domain.Node.PathPassthrough`, проходит весь путь
DTO → PostgreSQL → Redis-кеш → Receiver (читается в `nodecache` SELECT/Scan, т.к. решение про
приклеивание принимает Receiver). Для pull-узлов (RabbitMQAsync) сбрасывается в `NormalizeForRootMethod`
(нет входящего HTTP-пути).

### 39.2 Резолв узла: longest-prefix + remainder

`resolveNode` ([internal/receiver/usecase/resolver.go](../internal/receiver/usecase/resolver.go))
возвращает `(node, remainder, error)`:

1. **Точный матч** полного пути (обе интерпретации слога: `(team, path)` и legacy `("", team/path)`) —
   проверяется ПЕРВЫМ. Найден → `remainder = ""` (обычное поведение, в т.ч. для не-passthrough узлов).
2. При промахе — `prefixMatch`: идём по префиксам пути от длинного к короткому (по сегментам `/`,
   исключая полный путь). **Первый существующий узел-префикс = он.** Если у него `PathPassthrough = true`
   → возвращаем узел и `remainder` (отрезанный хвост). Если флаг выключен → `ErrNodeNotFound` и НЕ
   проваливаемся к более коротким префиксам (более длинный существующий узел «выигрывает» — нет
   footgun, когда `a/b` без passthrough и `a` с passthrough сосуществуют).

Хвост приклеивается к резолвнутому URL хелпером `appendPathSuffix` (`urlresolver.go`) через
`(*url.URL).JoinPath`: корректное кодирование сегментов + резолв `.`/`..` (защита от path-traversal
выше базового пути), query целевого URL сохраняется. Применяется и к `static`, и к `from_request`
(хвост клеится к любому резолвнутому адресу). Приклеивание происходит в Receiver (sync — в `route.go`,
async — в `route_async.go` перед `BuildEnvelope`); в Sender уходит уже готовый URL, **proto и Sender
для маршрутизации не меняются**.

Стоимость: до N вызовов `Get` для пути из N сегментов (каждый кешируется L2/Redis/PG). Обычный
exact-трафик = 1 `Get`. Trade-off: глубокий несуществующий путь даёт N промахов перед `404`.

### 39.3 Пересечение адресов узлов (детерминизм)

Узел `ozon` (passthrough) и узел `ozon/GetAuthToken` (точный) сосуществуют однозначно, т.к. exact
проверяется до prefix:

| Запрос | Срабатывает | Куда |
|---|---|---|
| `…/ozon/GetAuthToken` | точный узел `ozon/GetAuthToken` | его `target_url`, `method`(лог) пуст |
| `…/ozon/Foo` (точного нет) | `ozon` + passthrough, хвост `Foo` | `<target_ozon>/Foo`, `method`(лог)=`Foo` |
| `…/ozon` | точный узел `ozon`, хвост пуст | `<target_ozon>` как есть |

Точный узел «затеняет» ровно свой подпуть. Грабли оператора: если у узлов разные `target_url`, то
`GetAuthToken` поедет на target точного узла, а не на passthrough-результат. Предсказуемо
(longest-exact-wins), но создавать точный узел на подпуть, который должен проксироваться, не следует.

### 39.4 Лог-колонки: http_method (глагол) и method (подпуть)

При passthrough один узел обслуживает много подпутей (`v1/GetParcelsInfo`, `v1/NewPostings`…) в **одну**
CH-таблицу. Чтобы их различать, обязательная схема лог-таблицы (`RequiredLogColumns`, §4.3) меняется:

- добавлена колонка **`http_method String`** (сразу после `type`) — HTTP-глагол вызова (GET/POST/…),
  заполняется всегда;
- колонка **`method`** репурпозится — теперь хранит **подпуть запроса** (хвост passthrough), напр.
  `v1/GetParcelsInfo`; пусто у обычных узлов. До §39 здесь был HTTP-глагол.

Итоговый порядок (инвариант INSERT/SELECT): `ID, type, http_method, url, method, parameters, …`.
`domain.LogRecord` получает поле `HTTPMethod`; `Method` репурпозен. Проброс подпути из Receiver в
Sender — по образцу §37 (`node_id`): новое gRPC-поле `SendRequest.request_path = 29` (sync) и поле
`request_path` в Kafka-envelope (async/DLQ). В Sender кросс-маппинг: `in.Method`(глагол) → колонка
`http_method`, `in.RequestPath`(подпуть) → колонка `method`. Сам HTTP-вызов по-прежнему использует
глагол `in.Method`.

**Миграция существующих таблиц**: `EnsureHTTPMethodColumn`
([platform/clickhouse/ensure_schema.go](../internal/platform/clickhouse/ensure_schema.go)) —
идемпотентный `ALTER TABLE <t> ADD COLUMN IF NOT EXISTS http_method String DEFAULT '' AFTER type` на
старте и Web, и Sender (как §37). Колонка `method` уже есть — меняется только что в неё пишут для
нового трафика; старые записи остаются с прежним смыслом (как legacy `node_id=''`). Новые таблицы
получают колонку из `RequiredLogColumns` (`RenderCreateTable`).

### 39.5 Replay passthrough-запросов

Replay реинъектит запрос через входной endpoint Receiver по `node.Path`. Для passthrough-узла исходный
вызов бил в подпуть (сохранён в колонке `method` = `orig.Method`), поэтому реинъекция только по
`node.Path` ушла бы на корень узла. §39: при `node.PathPassthrough` и непустом `orig.Method`
`Dispatch.NodePath = node.Path + "/" + orig.Method` — Receiver переразрешит через `prefixMatch` обратно
на тот же узел + хвост. HTTP-глагол по-прежнему берётся из `node.IncomingMethod` (не из лога; §34.5),
репурпозинг `method` это не затрагивает.

### 39.6 UI

- **Форма узла**: переключатель «Проксировать хвост пути» (`path_passthrough`) в карточке «Куда
  перенаправить» под Target URL, для request/requestAsync (не для pull). По умолчанию выключен.
- **Вкладка логов**: отдельные колонки «HTTP» (глагол) и «Метод» (подпуть). У обычных узлов колонка
  подпути пуста.

### 39.7 Scope и неочевидности

**В scope:** флаг `path_passthrough`; longest-prefix-резолв + приклеивание хвоста (sync/async/callback);
оба URL-режима; лог-колонки `http_method`/`method` сквозняком (proto, envelope, writer, reader, миграция
таблиц в обоих сервисах); replay по полному подпути; UI (toggle + колонки логов).

- **Почему opt-in флаг, а не глобально:** универсальный passthrough сменил бы поведение существующих
  узлов (404 на лишний хвост → внезапное проксирование). Флаг по умолчанию off = нулевая регрессия.
- **Почему хвост приклеивается в Receiver, а не в Sender:** Sender получает уже финальный URL; так
  proto/Sender не зависят от логики passthrough. Для логирования подпути всё же добавлено proto-поле
  `request_path` (это про колонку, не про маршрут).
- **Почему `JoinPath`:** даёт кодирование + резолв `..` (клиент не выйдет за пределы базового пути).
- **Кросс-маппинг `proto.method`→колонка `http_method`:** имена колонок выбраны оператором
  (`method`=подпуть, `http_method`=глагол); в коде это документированный кросс-маппинг в `send.go`.

**Out of scope:** бэкфилл `method`/`http_method` старых записей; вторичный индекс; регекс-маршрутизация
подпутей; переписывание/маппинг хвоста (только дословное приклеивание).
