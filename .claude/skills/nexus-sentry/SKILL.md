---
name: nexus-sentry
description: Analyze, triage and fix Sentry errors and check Sentry performance for the Nexus bus. Covers where Sentry is wired (platform/sentry — Init, beforeSend/beforeSendTransaction scrubbing, GinMiddleware http.server transactions), the masking + body-size-cap contract that every event must keep, how to read issues from the self-hosted Sentry (sentry.vozovoz.ru, project 52) via the API with an auth token (the DSN cannot read issues), how to reproduce/fix an error within Clean Architecture, and how to read request-performance traces (and cross-check with Prometheus). Use when investigating a Sentry issue, adding/auditing data scrubbing, wiring new capture, or checking request latency/throughput in Sentry.
---

# Nexus — Sentry analysis, error fixing, performance

Sentry в Nexus поднимается одинаково в трёх сервисах через
[internal/platform/sentry/sentry.go](internal/platform/sentry/sentry.go) (`Init`) с DI-логгером
(`github.com/vsilchenkov/logging`). Конфиг — секция `sentry:` ([config.go](internal/platform/config/config.go)
`SentrySection`): `use`, `dsn`, `environment`, `level`, `enable_tracing`, `traces_sample_rate`. Hot-reload
через `Reload` (Redis pub/sub, §14.5). При `use=false` — полный no-op.

## 1. Где что (карта)

| Что | Файл |
|---|---|
| Init + scrubbing хуки | [platform/sentry/sentry.go](internal/platform/sentry/sentry.go) |
| Gin-middleware: транзакция `http.server` на запрос, тег `request_id`/`node`/`service` | [platform/sentry/middleware.go](internal/platform/sentry/middleware.go) |
| Сквозной `request_id` (UUID, `X-Request-Id`) — тег на scope hub'а | [platform/requestid](internal/platform/requestid/) + §30 |
| Кастомный gin-recovery (500 + лог + Sentry) | [platform/recovery](internal/platform/recovery/) |
| Маскирование чувствительных полей (`sensitiveKeys`) | sentry.go `isSensitive`/`maskMap`/`maskAny` |

## 2. Контракт scrubbing (НЕ ломать при правках)

Каждое событие чистится в `beforeSend` (ошибки) **и** `beforeSendTransaction` (performance —
отдельный хук, без него транзакции уходят неочищенными):

- **Маскирование по имени.** Значения полей/заголовков/тегов, чьё имя матчит `sensitiveKeys` (substring,
  case-insensitive), заменяются на `***`. **Добавил новое чувствительное поле — впиши его имя в
  `sensitiveKeys`** (CLAUDE.md). `event.Request.Data/Cookies/QueryString` стираются целиком.
- **Кап размера (§42).** Любая строка режется до `maxSentryValueBytes` (8 КиБ) по границе руны: `Message`,
  `Exception[].Value`, `Contexts` (slog кладёт сюда поля лога), `Breadcrumbs` (Message+Data), а в
  транзакциях — `Spans[]` (Description/Tags/Data). **Большое тело запроса/ответа НЕ должно попадать в
  Sentry** — кап это гарантирует даже при случайной утечке тела в текст ошибки. Тела логов живут только в
  ClickHouse (`rec.Request`/`rec.Response`), не в Sentry.

Тесты контракта — [sentry_test.go](internal/platform/sentry/sentry_test.go) (`isSensitive`, `maskMap`,
`TruncateValue`, `BeforeSend_TruncatesLargeBody`, `BeforeSendTransaction_ScrubsSpans`). Меняешь scrubbing
— обнови их.

## 3. Анализ ошибок: как читать issues из Sentry

**Важно:** DSN из URL (`https://<public_key>@sentry.vozovoz.ru/52`) — это **ingest**-ключ (отправка
событий), им **нельзя читать** issues. Для чтения нужен **Sentry API auth token** (User/Org token с
scope `project:read`, `event:read`).

> **Токен СОХРАНЁН в памяти** — `[memory] reference_sentry_api` (User Auth Token `sntryu_…`). Оттуда же
> берётся инстанс/org/project. Если токен протух — попроси новый у пользователя и обнови memory.

Инстанс: `https://sentry.vozovoz.ru`; org **`vzv`** («Возовоз»), project **`nexus`** (id `52`, platform
`go-gin`). API (`Authorization: Bearer <TOKEN>`):

```bash
TOKEN=...   # из [memory] reference_sentry_api (НЕ DSN)
BASE=https://sentry.vozovoz.ru/api/0
# топ незакрытых issue за 14 дней по частоте:
curl -s -H "Authorization: Bearer $TOKEN" \
  "$BASE/projects/vzv/nexus/issues/?query=is:unresolved&sort=freq&statsPeriod=14d" | jq '.[] | {id,title,count,culprit,permalink}'
# последнее событие issue (stacktrace, теги request_id/node/service, breadcrumbs):
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/issues/<issue_id>/events/latest/" | jq '{culprit, tags, entries}'
```

Из события бери: `culprit`/stacktrace → файл и `op` (логируется через `ErrorWithOp`), теги
`request_id`/`node`/`root_method`/`service` → какой узел/сервис/запрос, breadcrumbs → последовательность.
`X-Request-Id` коррелирует Sentry-событие с логами.

## 4. Как чинить ошибку

1. **Воспроизведи** в тесте (table-driven; mock через интерфейс). Ошибка из транспорта/инфры (gRPC
   `ResourceExhausted`, Kafka `MessageTooLargeError`, CH `code 60`) — ищи лимит/конфиг, не только код.
2. **Чини по слою** (Clean Architecture `handler→usecase→port→adapter`): не протаскивай тело/секреты в
   текст ошибки (кап их обрежет, но и не нужно). Ошибки — значения, `%w`, обрабатывать один раз.
3. **Гейты** (CLAUDE.md): `go build ./...`, `golangci-lint run`, `make test`, при инфре —
   `make test-integration`; `-race` в контейнере (память `project_race_via_docker`). Фронт-фиксы — браузер
   на стенде (память `feedback_browser_testing`).
4. **CHANGELOG** `[Unreleased]` (Fixed) — если ошибка пользовательски значима.

## 5. Метрики/производительность

- **Sentry performance** = транзакции `http.server` из `GinMiddleware`: длительность транзакции = латентность
  запроса; теги `service`/`node`/`root_method`; статус спана из HTTP-кода (`httpStatusToSpanStatus`).
  Сэмплирование — `traces_sample_rate` (дефолт 0.1), гейт `enable_tracing`. Инфра-пути (`/metrics`,
  `/health`, `/ready`) исключены (`skipSentry`), чтобы не зашумлять. Медленный эндпоинт → транзакции с
  высоким `transaction.duration`; группировка по route-шаблону (`SourceRoute`).
- **Источник истины по перфу — Prometheus** (always-on, не сэмплирован): `nexus_request_duration_seconds`
  (Sender, sync+async), панель метрик §21 (`/api/metrics/*`), Kafka-мониторинг §31. Sentry-трейсы —
  выборочная детализация поверх; для «запросы медленные/быстрые» сверяйся с Prometheus/панелью.
- Транзакции тоже чистятся (`beforeSendTransaction`) — большое тело/секрет в спанах не утекают.

## 6. Чек-лист «проанализировать Sentry»

- [ ] Есть auth-токен? Иначе запросить (DSN не годится).
- [ ] Топ issue за период по частоте; сгруппировать по `culprit`/`request_id`/`node`.
- [ ] Для каждого: воспроизвести → починить по слою → тест → гейты.
- [ ] Проверить, что фикс не добавил утечку (тело/секрет) в Sentry; при новом чувствительном поле — в
      `sensitiveKeys`.
- [ ] Performance: сверить медленные транзакции с Prometheus/панелью; убедиться, что инфра-пути исключены.
