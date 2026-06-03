## 30. Логирование, обработка паник и идентификация запросов

### 30.1. Зачем

Сервисы шины (Receiver / Sender / Web) — долгоживущие процессы. Паника в любой
горутине или HTTP-обработчике не должна ронять процесс: её нужно перехватить,
залогировать как `error` (а значит — отправить в Sentry) и продолжить работу.
Дополнительно каждому запросу нужен сквозной идентификатор (`request_id`), чтобы
связывать логи, Sentry-события и трейсы между сервисами. И версия приложения
должна быть видна в веб-интерфейсе.

Зависимость: логгер `github.com/vsilchenkov/logging` обновлён до **v1.7.9** —
в нём появились `Logger.With(...)` и `Logger.WithContext(ctx)`, и методы логируют
через `LogAttrs(ctx, ...)`. Благодаря этому контекст доходит до Sentry-хендлера
(`sentryslog`), и `logger.WithContext(reqCtx).Error(...)` отправляет событие в
request-scoped Sentry-hub (со всеми тегами запроса).

### 30.2. Перехват паник в горутинах — `safego`

Пакет [internal/platform/safego](../../internal/platform/safego/safego.go):

- `Recover(logger, op)` — defer-friendly: гасит панику, логирует `error`
  (логгер сам капчурит в Sentry, у фоновой горутины — в глобальный `CurrentHub`),
  не делает re-panic. Stacktrace кладётся атрибутом `stack`.
- `RecoverCtx(ctx, logger, op)` — вариант для горутин с контекстом, несущим
  Sentry-hub (через `logger.WithContext(ctx)`).

Правило: **каждая горутина в продакшн-коде** первой строкой ставит
`defer safego.Recover(<logger>, "<op>")`. Для пулов с `defer wg.Done()` — recover
ставится в коде **после** `wg.Done()`, чтобы выполниться первым (LIFO) и погасить
панику до отработки `wg.Done`.

Покрыты: главная app-горутина в [runner.go](../../internal/platform/runner/runner.go)
(главный пробел — паника тут шла мимо `bootstrap.Shutdown`), HTTP-listeners,
Redis pub/sub reloader, housekeeping, Kafka lag reporter, gRPC/admin-серверы
Sender, Kafka consumer-пул, ClickHouse writer + file-fallback, Puller-воркеры
RabbitMQAsync, delayed CH-close, `TouchLastUsed`, SSE live-tail. Точки входа
production-сервисов (`cmd/{receiver,sender,web}`) уже защищены
`defer bootstrap.Shutdown(logger)` в `main`; dev-утилиты `echosrv`/`loadtest`
получили `safego.Recover` в `main`, `rotate-key` — под `bootstrap.Shutdown`.

### 30.3. Идентификация запросов — `request_id`

Пакет [internal/platform/requestid](../../internal/platform/requestid/requestid.go):

- `GinMiddleware()` читает заголовок `X-Request-Id`; при отсутствии/пустом —
  генерирует UUID v4 (`google/uuid`). **Существующий не перезаписывает** (для
  сквозной трассировки между сервисами). Кладёт id в контекст запроса и
  `gin.Context`, выставляет заголовок ответа `X-Request-Id`.
- `FromContext(ctx) string` / `WithValue(ctx, id)` — доступ к id.

Middleware стоит **первым** в цепочке — id доступен всем нижележащим.

В Sentry id попадает из [sentry/middleware.go](../../internal/platform/sentry/middleware.go):
тег `request_id` ставится на **scope** склонированного hub'а (⇒ во все события
запроса, включая залогированные через `logger.WithContext`) и на **транзакцию**
(⇒ в трейсы). `request_id` не входит в `sensitiveKeys`, поэтому не маскируется.

### 30.4. gin recovery

Встроенный `gin.Recovery()` (пишет в свой stderr-writer, без нашего логгера и
Sentry) заменён на [recovery.GinMiddleware](../../internal/platform/recovery/middleware.go)
во всех трёх движках. При панике в обработчике: лог через
`logger.WithContext(reqCtx).Error(...)` (capture в request-scoped hub с
`request_id`/`service`/`node`), ответ клиенту **500 JSON** `{error, request_id}`.

Порядок цепочки: `requestid → otel → sentry → recovery → metrics [→ i18n]`.
recovery стоит **после** sentry (чтобы паника гасилась до возврата в
sentry-middleware и его span получил статус 500) и **раньше** metrics/handler'ов
(чтобы ловить их паники).

### 30.5. Версия приложения в UI

- Backend: публичный (без авторизации) `GET /api/version` →
  [version_handler.go](../../internal/web/adapter/in/http/version_handler.go),
  отдаёт `{"version": cfg.Build.Version}`. Регистрируется на корневом движке
  (вне auth-группы), swagger-тег `meta`.
- Frontend: [Sidebar.tsx](../../web-ui/src/components/Sidebar.tsx) запрашивает
  `/api/version` (`staleTime: Infinity`) и показывает `v{version}` в футере под
  пользователем.
- Источник версии и порядок присвоения новой — см. «Версионирование» в
  [DEPLOYMENT.md](../../DEPLOYMENT.md).

### 30.6. Out of scope (v1)

- Per-request обогащение логгера `request_id` во **всех** обработчиках (ядро —
  request_id в трейсах/событиях паник — закрыто; точечный ретрофит логов
  handler'ов через `logger.WithContext` — по необходимости).
- Структурный stacktrace паники как отдельный Sentry-issue с группировкой
  (используется capture через `logger.Error`, stacktrace — в атрибуте `stack`).
- Распространение `request_id` в исходящие HTTP-запросы к целевым узлам (можно
  добавить как forward-header через §24 каталог).
