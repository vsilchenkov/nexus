# 52. Трёхсостоянье статуса узла: OK / Degraded / Down

Раздел закрывает ложную тревогу «живой узел горит Down» и вводит третье состояние runtime-статуса
узла на дашборде. Развивает §41 (бейдж «Down» по Prometheus-гауджу), §46 (персистентный исход
последнего вызова в Redis) и §50.4 (circuit breaker не открывается по 4xx).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 52.1 Проблема: 4xx красит живой узел в Down

**Симптом (боевой, продолжение инцидента §50.4, узел `site/push`, 15.07.2026):** рассылка пушей
попадала на протухшие FCM-токены → push-ms отвечал `422 NotRegistered`. После фикса §50.4 breaker
по 4xx больше не открывается (валидные пуши доставляются), но бейдж узла на Overview всё равно
**Down**: флаг «последний вызов ошибочен» — булев и означает «любой не-2xx»
(`isErr := StatusCode < 200 || >= 300` в [sender_service.go](../../internal/sender/adapter/in/grpc/sender_service.go)
и [async.go](../../internal/sender/usecase/async.go)). Оператор видит красный узел, бежит проверять —
а узел жив и отвечает; это ложная тревога, требующая внимания при каждой серии клиентских ошибок.

## 52.2 Модель: три состояния исхода последнего вызова

Единая доменная модель `domain.NodeOutcome`
([internal/domain/node_outcome.go](../../internal/domain/node_outcome.go)) — исход **последнего**
исходящего вызова узла:

| Состояние  | Условие                                             | Бейдж Overview |
|------------|-----------------------------------------------------|----------------|
| `ok`       | ответ 2xx                                           | OK (зелёный)   |
| `degraded` | узел ответил, но не-2xx и < 500 (1xx/3xx/4xx)       | Degraded (жёлтый) |
| `down`     | транспортная ошибка (status 0) **или** ответ >= 500 | Down (красный) |

Граница `< 500` = `upstreamHealthy` из §50.4
([send.go](../../internal/sender/usecase/send.go), точка решения breaker'а): «узел жив и отвечает».
Классификация — по `SendOutput.StatusCode` (он полностью детерминирует исход; отдельного поля в
`SendOutput` не заводим):

| StatusCode              | Outcome    | Комментарий                                           |
|-------------------------|------------|-------------------------------------------------------|
| 200..299                | `ok`       |                                                       |
| 1xx, 3xx, 4xx (вкл. 422)| `degraded` | ошибка данных/клиента; узел жив (§50.4)               |
| <= 0                    | `down`     | транспортная ошибка (timeout/refused), статус не получен |
| >= 500                  | `down`     | ошибка сервера узла                                   |
| 503 breaker-open        | `down`     | шина не вызывала upstream — доставка не происходит    |
| 502 oversize (§43-rev)  | `down`     | ответ отброшен по транспортному лимиту — недоставка   |

**Принятое отклонение от «чистого» upstreamHealthy:** breaker-open (503) и oversize (502)
синтезируются самой шиной, breaker их источником нездоровья не считает, но для бейджа это `down` —
полезная нагрузка не доставляется. Расхождение намеренное: breaker меряет здоровье транспорта,
бейдж — доставляемость.

Семантика прежнего булевого флага сохраняется методом `IsError()` (`outcome != ok`) — им живут
`nexus_request_incomplete_total` и back-compat поле API `last_error`.

## 52.3 Кодировки: gauge и Redis (намеренно разные)

Пишущая сторона — обе точки записи Sender (sync [sender_service.go](../../internal/sender/adapter/in/grpc/sender_service.go),
async [async.go](../../internal/sender/usecase/async.go)) классифицируют исход один раз и пишут в
два стора:

**Prometheus gauge `nexus_node_last_request_error{node}`** (§41) — имя сохранено (внешние
дашборды/алерты), значения расширены: **0 = ok, 1 = degraded, 2 = down**.
`SetNodeLastRequestError(node, bool)` → `SetNodeLastRequestOutcome(node, NodeOutcome)`
([platform/metrics](../../internal/platform/metrics/metrics.go)). PromQL-читатель
`max by (node)(...)` ([web prometheus client](../../internal/web/adapter/out/prometheus/client.go))
не меняется: `max` теперь выбирает **худшее** состояние между репликами Sender. Старые алерты
`>= 1` продолжают ловить «любую проблему»; порог «только down» — `>= 2`.

**Redis `nexus:node:last_error:<path>`** (§46, ключ и TTL 30 суток не меняются) — значения:
**`"0"` = ok, `"1"` = down, `"2"` = degraded**. Кодек `EncodeOutcome`/`DecodeOutcome` живёт только в
[platform/nodestatus](../../internal/platform/nodestatus/redis.go).

**Грабли: кодировки gauge и Redis НАМЕРЕННО разные** (в gauge degraded=1/down=2, в Redis
down=1/degraded=2). Причины — у сторов разные ограничения:

- gauge требует **порядка** (down > degraded > ok), чтобы `max by (node)` выбирал худшее;
- Redis требует **legacy/rolling-совместимости** со старым булевым значением `"1"` = «любой не-2xx».

Матрица совместимости при rolling-деплое:

| Писатель \ Читатель | Старый Web (`s == "1"` → err)      | Новый Web (кодек)          |
|---------------------|-------------------------------------|----------------------------|
| Старый Sender (`"1"` = любой не-2xx) | как сейчас         | `"1"` → **down** (worst case; самоисцелится следующим вызовом узла) |
| Новый Sender        | `"1"`(down) → красный ✓; `"2"`(degraded) → OK (транзиентно, даже ближе к целевому поведению) | штатно |

Нераспознанное значение Redis → узел пропускается (fallback на Prometheus, как сейчас для
отсутствующего ключа). Легаси-грабля gauge: старый Sender при 5xx писал 1 → новый Web истолкует как
degraded до следующего вызова узла — транзиентно, Redis приоритетен.

## 52.4 Порты и читающая сторона (Web)

- Sender-порт (consumer-side, [async.go](../../internal/sender/usecase/async.go)):
  `NodeStatusWriter.SetLastError(ctx, path, bool)` → `SetLastOutcome(ctx, path, NodeOutcome)`;
  реализации `nodestatus.RedisWriter` и `nodestatus.Noop`.
- Web-порт ([port/metrics_provider.go](../../internal/web/usecase/port/metrics_provider.go)):
  `NodeStatusReader.GetLastErrors` → `GetLastOutcomes(ctx, paths) (map[string]NodeOutcome, error)`;
  узлы без ключа/с нераспознанным значением в карту не попадают (добор из Prometheus).
- `PromMetrics.NodeLastErrors` (map[string]float64) — **не меняется** (сырой gauge); маппинг
  float → outcome в usecase через `OutcomeFromGaugeValue` (>=2 → down, >=1 → degraded, иначе ok).
- Usecase ([metrics.go](../../internal/web/usecase/metrics.go)): `NodeThroughputRow.LastError bool`
  → `LastOutcome NodeOutcome`; `applyLastErrors` → `applyLastOutcomes` (приоритет Redis → fallback
  Prometheus → `ok`).

## 52.5 API

`GET /api/metrics/nodes`, элемент `items[]`
([metrics_handler.go](../../internal/web/adapter/in/http/metrics_handler.go)):

- `last_error bool` — **сохраняется** (back-compat для внешних потребителей с `metrics:read`);
  семантика прежняя «любой не-2xx» = `last_outcome != "ok"`;
- `last_outcome string` — **новое**: `"ok" | "degraded" | "down"`; при отсутствии данных — `"ok"`
  (эквивалент прежнего `last_error=false`; UI гасит неопределённость через `metricsReady`).

Замена поля отвергнута — breaking change без выгоды. Swagger регенерируется (`make swagger`);
попутно устранён дрейф `NodesMetricsResponse` (отсутствовало реально отдаваемое поле `totals`).

## 52.6 UI (Overview)

- Новый `Variant` **`degraded`** — отдельный от занятого `warn` (= Queue, отставание async-очереди).
- `nodeVariant`: `paused/disabled/unknown` (как было) → `lastOutcome === "down"` → `err` →
  `lastOutcome === "degraded"` → `degraded` → queue-проверка → `ok`.
- Бейдж: тон `warn` (жёлтый, `Pill` уже умеет), подпись `overview.status.degraded` = «Degraded»
  (латиницей в обеих локалях — консистентно с OK/Down).
- Сортировка «проблемные первыми»: `err:0, degraded:1, warn:2, paused:3, ok:4, unknown:5, disabled:6`;
  акцент карточки `border-l-warn`; в фильтр статусов добавляется опция Degraded.
- `client.ts`: `Throughput.lastOutcome` из `last_outcome`; `last_error` в типе API остаётся
  (контракт), UI им больше не пользуется.

## 52.7 Тестовый контур

- **Юнит:** таблица классификатора `OutcomeFromStatusCode` (границы 0/-1/101/200/204/299/301/404/
  422/499/500/502/503/599) и `GaugeValue`/`OutcomeFromGaugeValue` round-trip; кодек Redis
  (вкл. legacy `"1"` → down, мусор → не-ок); gauge-сеттер (0/1/2 + перезапись); **новый** тест
  gRPC-адаптера Sender (не был покрыт вовсе): реальный `SendUsecase` со стабами, таблица
  200/302/422/500/транспорт → ожидаемые gauge, `SetLastOutcome` и «`incomplete_total` только при
  не-2xx»; async-путь — расширение `TestAsync_WritesNodeStatus` теми же исходами + ассерт, что
  Ack/DLQ-решение не изменилось; web-usecase — tri-state приоритет Redis над Prometheus, fallback
  1→degraded/2→down/0→ok, деградация при ошибке Redis.
- **Интеграционные (сценарные), реальный Redis (testcontainers):** round-trip writer→reader для всех
  трёх значений + legacy `"1"` + перезапись down→ok
  ([tests/integration/nodestatus_test.go](../../tests/integration/nodestatus_test.go)); сценарий
  инцидента §50.4 — серия 422 → reader даёт `degraded`, затем 500 → `down`, затем 200 → `ok`
  (через реальный `AsyncProcessor.Handle` + Redis writer/reader).
- Полный E2E через gRPC-сервер Sender **не делается**: адаптер — тонкий маппинг, покрыт юнит-тестом
  с реальным usecase; транспорт gRPC покрыт §-тестами Receiver; полный стенд потребовал бы CH/Kafka
  без прироста покрытия именно §52.

## 52.8 Down — не «последний вызов упал», а «упало N подряд»

### Что было не так

Исход писался по КАЖДОМУ вызову, поэтому первая же 500 переводила узел в `down`. На узле с
постоянным потоком это давало картину, которая не бьётся с логами: бейдж «Down», а в журнале
подряд успешные ответы.

Боевой случай (20.08.26, узел `edo-sbis`): за час 489 входящих и 42 ошибки, узел показан Down. В
секунде 09:31:01 три запроса — два ответа 200 по ~35 мс и один 500 за 1493 мс. Последним
**завершился** именно 500 (он стартовал в ту же секунду, но длился в сорок раз дольше), он и
перезаписал статус. Журнал при этом отсортирован по времени НАЧАЛА, поэтому «два последних
успешных» и «последний завершившийся» — разные запросы, и объяснить бейдж по экрану невозможно.

### Модель

«Тяжёлые» отказы (транспортная ошибка или 5xx — то, что §52.2 относит к `down`) копятся в счётчике
подряд идущих неудач:

| Исход вызова | Счётчик | Итоговый статус |
|---|---|---|
| 2xx | обнуляется | `ok` |
| 4xx (§52.1) | не меняется | `degraded` |
| 5xx / нет ответа, счётчик < порога | +1 | `degraded` |
| 5xx / нет ответа, счётчик ≥ порога | +1 | `down` |

Порог — `sender.node_down_threshold`, по умолчанию **10**. Значение 1 возвращает прежнее поведение.

4xx счётчик намеренно не трогает: это ответ приёмника, а не его отказ, и поток клиентских ошибок не
должен «ронять» живой узел. Обнуление успехом делает статус описанием ТЕКУЩЕГО состояния, а не суммы
за всё время.

### Реализация

Счётчик живёт отдельным ключом `nexus:node:fail_streak:<path>` с тем же TTL, что и статус: формат
значения исхода (§52.3) не меняется, и читатели любой версии продолжают работать.

Инкремент и решение выполняются **одним Lua-скриптом**: при двух репликах Sender (§93) запросы
одного узла идут через обе, и последовательность «прочитал → посчитал → записал» теряла бы отказы —
узел не доходил бы до порога никогда.

`SetLastOutcome` возвращает ЭФФЕКТИВНЫЙ исход, и по нему же выставляется Prometheus-гаудж
`nexus_node_last_request_error` (§41). Иначе бейдж (Redis) и алерты (Prometheus) трактовали бы один
узел по-разному: интерфейс показывал бы degraded, а алерт уже сработал бы на down.

Без Redis (`nodestatus.Noop`) порог не применяется — счётчик хранить негде, поведение остаётся
прежним.

## 52.9 Спарклайн карточки показывает данные, а не статус

Второй половиной той же жалобы был график: у узла со статусом Down все столбцы спарклайна на
рабочем столе (§22/§44) горели красным, хотя из 489 запросов ошибочными были 42.

Причина: `Sparkline` красил ВСЕ столбцы одним цветом по статусу узла, а высота столбца означала
общее число входящих. Смысл цвета и смысл высоты разъехались — «сколько упало» не рисовалось вообще.

Теперь столбец двухцветный, как на графике узла (§79.5): красный сегмент по доле ошибок в бакете,
остальное — обычный цвет. Данные для этого уже возвращал `NodeChart` (`SeriesPoint.Errors`), их
просто не отдавали клиенту; добавлено поле `spark_err` рядом с `spark` (аддитивно, старый клиент не
ломается). Пустой `spark_err` означает «разбивки нет» (Prometheus-fallback) и рисуется одноцветным
столбцом, а не «ошибок ноль».

Статус узла остаётся в бейдже и в левой полосе карточки. Приглушённый цвет сохраняется только там,
где данных нет или узел выключен: `paused`, `disabled`, `unknown`.

## 52.x Scope (что не входит)

- `nexus_request_incomplete_total` не меняется — по-прежнему «любой не-2xx» (метрика о
  недоставленности, 4xx туда входит).
- Счётчики «Ошибки» дашборда (§44, по `done` в ClickHouse) не меняются: 4xx остаётся ошибкой
  доставки. §52 — только про runtime-бейдж узла.
- DLQ-reprocess ([dlq_reprocess.go](../../internal/sender/usecase/dlq_reprocess.go)) не пишет статус
  узла — **существующий** пробел (исходы повторной доставки не обновляют бейдж), сознательно
  оставлен как есть; кандидат в follow-up.
- Runtime-бейдж на странице узла (NodeDetail) — там только конфигурационный статус
  (enabled/paused/disabled); добавление runtime-статуса — возможный follow-up.
  **Реализовано в [§84.7](84-node-observability.md).** Приоритет Redis → Prometheus вынесен в общий
  резолвер, у узла появился собственный узкий эндпоинт; `ok` бейджа не рисует, «неизвестно» не
  красится в `ok`.
- Telegram-алерты §22 не трогаются (идут от счётчиков ошибок, не от gauge последнего исхода).
- Per-node история переходов ok↔degraded↔down (timeline) — вне scope.
