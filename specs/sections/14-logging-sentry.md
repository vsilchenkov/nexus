## 14. Логирование и Sentry

### 14.1 Пакет логирования

Оба сервиса (Receiver, Sender, Web) используют общий пакет `github.com/vsilchenkov/logging` — обёртка над `log/slog` с цветным выводом в stderr (через `tint`) или JSON в файл, и опциональным мульти-хендлером в Sentry.

> **Главный принцип: dependency injection.**
> Логгер инициализируется ровно один раз — в `main`. Дальше он передаётся в каждую структуру явно через конструктор и хранится как поле. Никаких `logging.GetLogger()` или других пакетных глобалов в продакшн-коде. Это правило не обсуждается — оно определяет всё остальное в этом разделе.

**Инициализация.** Логгер создаётся ровно один раз в `main` каждого сервиса вызовом `logging.Initlogger(cfg, sentryCfg)`. Полученный `*Wrappedlogger` передаётся в конструкторы всех подсистем (сервер, handlers, хранилища, gRPC-клиенты) как зависимость. Получать логгер через `logging.GetLogger()` из произвольного места кода **не допускается** — это создаёт скрытую глобальную зависимость, ломает тестируемость и противоречит принципу явной передачи зависимостей.

**Структура `logging.Config`:**
- `Debug` (bool) — режим отладки.
- `Level` (int) — уровень логирования: `2` = error, `3` = warn, `4` = info (по умолчанию), `5` = debug.
- `OutputInFile` (bool) — писать в файл вместо stderr.
- `Dir` (string) — каталог логов (относительно `WorkingDir`), файл `app.log`.
- `BuildConfig`:
  - `Version` — версия сервиса (из git tag или ldflags).
  - `ProjectName` — имя сервиса (`receiver`, `sender`, `web`).
  - `WorkingDir` — рабочий каталог.

**Стиль передачи через DI.** Каждая структура, которая что-то логирует, держит логгер в публичном или приватном поле и принимает его в конструкторе:

```go
// cmd/receiver/main.go
func main() {
    cfg := config.Load()
    logger := logging.Initlogger(&cfg.Logging, &cfg.Sentry)

    logger.Info("starting receiver",
        logger.Str("version", cfg.Build.Version),
        logger.Str("port", cfg.Receiver.HttpAddr))

    pgPool := storage.NewPostgres(cfg.Postgres, logger)
    redisClient := storage.NewRedis(cfg.Redis, logger)
    chLogger := storage.NewClickHouse(cfg.ClickHouse, logger)
    senderClient := grpcclient.NewSender(cfg.Receiver.SenderGRPC, logger)

    srv := server.New(cfg.Receiver, pgPool, redisClient, chLogger, senderClient, logger)
    if err := srv.Run(ctx); err != nil {
        logger.ErrorWithOp("server stopped", err, "main")
        os.Exit(1)
    }
}

// internal/receiver/server.go
type Server struct {
    cfg    Config
    pg     *storage.Postgres
    logger *logging.Wrappedlogger
    // ...
}

func New(cfg Config, pg *storage.Postgres, /* ... */, logger *logging.Wrappedlogger) *Server {
    return &Server{cfg: cfg, pg: pg, logger: logger /* ... */}
}

func (s *Server) handleRequest(c *gin.Context) {
    s.logger.Info("request received",
        s.logger.Str("node", c.Param("path")),
        s.logger.Str("method", "request"),
        s.logger.Int("body_size", int(c.Request.ContentLength)))
}
```

В именовании поля придерживаемся одного стиля по всему репозиторию: либо `logger` (приватное), либо `Logger` (публичное, если структура используется как value-object из других пакетов). Выбор фиксируется на старте проекта и не смешивается.

**Стиль вызова.** Использовать типизированные хелперы атрибутов из самого `Wrappedlogger` — `Str/Int/Float64/Any/Err/Op` — а не `slog.String(...)` напрямую. Хелперы вызываются на том же логгере, что и метод записи:

```go
p.Logger.Info("server started",
    p.Logger.Str("port", p.port),
    p.Logger.Str("version", version),
    p.Logger.Str("build", fmt.Sprintf("%v", build)))

h.logger.Error("file processing failed",
    h.logger.Str("name", fileName),
    h.logger.Err(err))

s.logger.ErrorWithOp("kafka publish failed", err, "sender.publishAsync",
    s.logger.Str("topic", "nexus.async"),
    s.logger.Str("node", nodePath))
```

`ErrorWithOp` обязателен для ошибок — он добавляет в запись атрибут `op` (операция, в которой произошла ошибка) и упрощает группировку как в логах, так и в Sentry. `op` пишется в формате `<пакет>.<функция>` — например, `sender.publishAsync`, `storage.postgres.findNode`.

**Тестирование.** Для unit-тестов в конструктор передаётся либо `Wrappedlogger`, инициализированный с отбрасыванием вывода (`io.Discard`), либо mock-логгер. Никаких глобальных перехватов не требуется.

**Что и где логируется:**
- HTTP-запросы к Receiver (вход, маршрутизация, статус ответа) — на уровне `info`, тела не пишем (тело уходит в ClickHouse, см. §4.3).
- gRPC-вызовы между Receiver и Sender — `info` (старт + завершение).
- Kafka produce / consume — `info`; ошибки доставки и переход в DLQ — `error` с `op`.
- Сбои ClickHouse-логгера — `warn` (буфер растёт) / `error` (буфер переполнен, сброс в файл).
- Все panic — recover + `error` через `ErrorWithOp`, далее пробрасывается в Sentry (см. §14.2).

### 14.2 Подключение к Sentry

Используется тот же пакет — Sentry конфигурируется через `logging.SentryConfig` и подключается в `Initlogger` как дополнительный slog-handler. Никаких отдельных вызовов `sentry.Init` в коде не требуется.

**Структура `logging.SentryConfig`:**
- `Use` (bool) — мастер-переключатель. Если `false`, Sentry полностью отключён, логи идут только в stderr/файл.
- `Dsn` (string) — DSN проекта.
- `Environment` (string) — `production`, `staging`, `dev` и т.п.
- `Level` (int) — минимальный уровень событий, отправляемых в Sentry: `2` (error, по умолчанию) или `3` (warn+error). Уровни `info` и `debug` в Sentry не отправляются — Sentry для ошибок, не для трассировки.
- `AttachStacktrace` (bool) — прикреплять стектрейс к каждому событию.
- `TracesSampleRate` (float64) — доля транзакций для performance-мониторинга (0.0–1.0). См. §14.3.
- `EnableTracing` (bool) — включает performance-мониторинг (транзакции). Без него отправляются только ошибки.
- `Debug` (bool) — режим отладки самого Sentry SDK.
- `BuildConfig` — те же `Version`, `ProjectName`, `WorkingDir`. Передаются Sentry как `release` и `server_name` для корректной группировки и source-map-привязки.

**Рекомендуемые настройки по окружениям:**

| Окружение | `Use` | `Level` | `EnableTracing` | `TracesSampleRate` |
|---|---|---|---|---|
| dev | false | 2 | false | 0.0 |
| staging | true | 3 | true | 1.0 |
| production | true | 2 | true | 0.1 |

В production семплируем 10% транзакций — этого хватает для performance-картины и не разоряет квоту Sentry. В staging — 100%, чтобы видеть все.

**Конфигурация через env:** все поля `SentryConfig` пробрасываются переменными вида `SENTRY_DSN`, `SENTRY_ENVIRONMENT`, `SENTRY_LEVEL`, `SENTRY_ENABLE_TRACING`, `SENTRY_TRACES_SAMPLE_RATE`, `SENTRY_ATTACH_STACKTRACE`. Это позволяет менять режим без пересборки образа.

**Безопасность.** В Sentry не должны попадать:
- Тела запросов (`request` / `response` из таблиц ClickHouse) — там могут быть PII.
- Значения заголовков `Authorization`, `Cookie`, `X-Api-Key`, `X-Auth-Token`.
- Пароли пользователей UI и креды ClickHouse / внешних узлов.

Реализация: использовать `BeforeSend` хук Sentry SDK (либо настроить его внутри `logging` обёртки, либо добавить middleware) — он стирает поля с подозрительными ключами перед отправкой. Также вешается `BeforeBreadcrumb` для логов уровней ниже error, чтобы они хоть и попадали как breadcrumbs, но без чувствительных полей.

### 14.3 Транзакции (performance)

Если `EnableTracing = true`, оба сервиса оборачивают ключевые операции в Sentry-транзакции, чтобы видеть, на каком этапе тратится время.

**Receiver Service:**
- Транзакция на каждый входящий HTTP-запрос с именем `POST /v1/{root_method}/*` (название узла идёт тегом).
- Спаны: `auth.check` (проверка входящей авторизации), `db.lookup_node` (поиск конфига в PostgreSQL), `kafka.produce` (для async) или `grpc.send_to_sender` (для sync).

**Sender Service:**
- Транзакция на каждый gRPC-вызов от Receiver и на каждое сообщение из Kafka.
- Спаны: `http.outbound` (вызов внешнего узла), `clickhouse.log` (запись лога — в режиме исключения, по умолчанию батчинг асинхронный и в трассировку не идёт).

**Web Service:**
- Транзакции на API-эндпоинты `/api/nodes`, `/api/users`, `/api/settings/clickhouse/check` и т.п.
- Спан `db.query` оборачивает каждый запрос к PostgreSQL.

**Теги для транзакций:** `service` (`receiver` / `sender` / `web`), `node` (имя узла), `root_method` (`request` / `requestAsync`), `release` (версия из `BuildConfig`).

### 14.4 Настройки Sentry в Web UI (Settings → Sentry)

Раздел `Sentry` в боковой навигации настроек (см. §7.7) позволяет менять параметры отправки ошибок и performance-трассировок без рестарта сервиса:

- *Включить отправку в Sentry* — toggle (`Use`).
- *DSN* — текстовое поле, маскируется как пароль.
- *Environment* — селект (`production` / `staging` / `dev`) или свободный ввод.
- *Уровень событий* — `Только ошибки` (`Level = 2`) / `Предупреждения и ошибки` (`Level = 3`).
- *Прикреплять стектрейс* — чекбокс.
- *Включить транзакции (performance)* — toggle (`EnableTracing`).
- *Sample rate транзакций* — слайдер 0–100%, шаг 5%.
- Кнопка **«Отправить тестовое событие»** — отправляет в Sentry синтетический event уровня `error` для проверки настроек.
- Статус подключения: `Подключено` (зелёный) / `Не настроено` (серый) / `Ошибка` (красный с текстом).

Сохранённые настройки попадают в таблицу `app_settings` в PostgreSQL и применяются переинициализацией логгера без рестарта сервиса.

### 14.5 Хранилище настроек

Добавляется таблица `app_settings` в PostgreSQL (одна строка-singleton с JSON-полем `value`, либо классическое key-value):

- `logging.level`, `logging.output_in_file`, `logging.dir`
- `sentry.use`, `sentry.dsn`, `sentry.environment`, `sentry.level`, `sentry.attach_stacktrace`, `sentry.enable_tracing`, `sentry.traces_sample_rate`

При старте приложение читает env, затем поверх накладывает значения из БД (если они есть). Это позволяет первичный запуск делать только через env, а оперативно крутить настройки — из UI.

