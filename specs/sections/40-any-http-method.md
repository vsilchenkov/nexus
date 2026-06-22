## 40. HTTP-метод «Любой» (ANY) для входящего и исходящего метода узла

До §40 входящий и исходящий HTTP-методы узла (§3.2) ограничивались `GET/POST/PUT/DELETE`. Нельзя было
сделать узел, который принимает запрос **любым** методом и переотправляет получателю **тем же** методом
(прозрачный проксинг метода: PUT→PUT, DELETE→DELETE). §40 добавляет значение **`ANY`** («Любой») для
обоих полей.

### 40.1 Семантика

- **Входящий `ANY`** — узел принимает запрос с **любым** HTTP-методом, без `405`. `methodMatches` для
  `want=ANY` возвращает `true` для любого входящего метода.
- **Исходящий `ANY`** — Sender вызывает получателя **тем же методом, что пришёл от клиента** (зеркало
  источника). Резолв в Receiver через `effectiveOutgoingMethod(node, in.Method)`: при `OutgoingMethod=ANY`
  → метод входящего запроса (апперкейс; пустой → `POST`); иначе — сконфигурированный метод (как раньше).
- Комбинация **вх=ANY + исх=ANY** = полный прозрачный проксинг метода. Совмещается с path-passthrough
  (§39): `PUT …/node/sub` → `PUT <target>/sub`.

### 40.2 Где применяется

- `internal/domain/enums.go` — `HTTPMethodAny = "ANY"`, добавлен в `HTTPMethod.Valid()`.
- `internal/receiver/usecase/route.go` — `methodMatches` (вх ANY = accept-all) + `effectiveOutgoingMethod`
  (исх ANY = зеркало); вызов в sync (`SendRequest.Method`) и async
  (`route_async.go` → `BuildEnvelope`). Фактический метод попадает и в gRPC/envelope, и в лог-колонку
  `http_method` (§39) — консистентно.
- `internal/receiver/usecase/puller.go` — для pull-узлов (RabbitMQAsync) входящего HTTP-метода нет,
  зеркалить нечего → исходящий ANY трактуется как `POST`.
- `internal/web/usecase/replay.go` — у узла с `IncomingMethod=ANY` нет единственного метода для
  реинъекции; берётся залогированный глагол исходного запроса (`orig.HTTPMethod`, колонка `http_method`
  §39; fallback `POST`), а не литерал `ANY`.

### 40.3 Хранение, API, миграция

- DB: значение хранится строкой `'ANY'`. Миграция `0020_node_method_any` пересоздаёт CHECK-констрейнты
  `nodes_incoming_method_check` / `nodes_outgoing_method_check` с включением `'ANY'` (аддитивно;
  существующие `GET/POST/PUT/DELETE` остаются валидны). Дефолт колонок — по-прежнему `POST`.
- API: DTO `incoming_method`/`outgoing_method` — `oneof=GET POST PUT DELETE ANY`.
- UI: в селектах входящего/исходящего метода добавлен пункт со значением `ANY` и лейблом «Любой»/«Any»
  (`node.method.any`); help-тексты полей дополнены пояснением.

### 40.4 Scope и неочевидности

**В scope:** значение ANY для вх/исх метода (HTTP-узлы request/requestAsync); зеркалирование исходящего;
accept-all входящего; pull-узлы (исх ANY → POST); replay ANY-узла по залогированному методу; миграция
CHECK; DTO/UI/swagger.

- **Почему `ANY` хранится строкой, а не отдельным флагом:** значение метода уже строковый enum в одной
  колонке — `ANY` встаёт в общий ряд без новых полей.
- **Почему резолв исходящего в Receiver, а не в Sender:** только в Receiver доступен метод входящего
  запроса (`in.Method`); Sender и proto получают уже конкретный метод и не зависят от логики ANY.
- **Pull (RabbitMQAsync):** входящего HTTP-метода физически нет — `ANY` исходящего безопасно сводится к
  `POST` в puller'е.
- **Replay:** реинъекция через входной endpoint требует конкретного глагола — `ANY` непригоден, поэтому
  берётся реальный метод из лога (`http_method`).

**Out of scope:** методы вне HTTP-глаголов (CONNECT/TRACE/кастомные) — `ANY` принимает их на входе, но
маппинг/ограничения за пределами стандартных не вводятся.
