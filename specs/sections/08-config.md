## 8. Конфигурация приложения

### 8.1 Файлы конфигурации

Базовые настройки каждого сервиса хранятся в YAML-файлах в каталоге `/config` репозитория:

- **`config/config.yml`** — основной файл для production. Не содержит секретов: все чувствительные значения (пароли, DSN, токены) подставляются из env-переменных через подстановки `${VAR}` или `${VAR:default}`.
- **`config/config_debug.yml`** — конфигурация для локальной отладки. `Debug: true`, уровень логирования 5 (debug), Sentry выключен (`Use: false`), увеличенные таймауты, локальные адреса зависимостей (`localhost:5432`, `localhost:9092` и т.п.).
- **`config/config.example.yml`** — закоммиченный шаблон с пояснениями всех полей и примерами значений. `config.yml` в git не коммитится, добавляется в `.gitignore`.

### 8.2 Выбор конфига

Источник конфига определяется в порядке приоритета:

1. **Флаг командной строки** `--config /path/to/file.yml` (или короткий `-c`) — высший приоритет.
2. **Переменная окружения** `NEXUS_CONFIG=/path/to/file.yml`.
3. **Дефолт** — `./config/config.yml` относительно `WorkingDir`.

Каждый бинарь (`receiver`, `sender`, `web`) принимает один и тот же набор флагов: `--config`, `--debug` (форсирует загрузку `config_debug.yml`), `--version` (печатает версию из `BuildConfig`).

### 8.3 Структура config.yml

Единый файл для всех сервисов с секциями по каждому сервису. Каждый бинарь читает общие секции (`logging`, `sentry`, `postgres`, `clickhouse`, `kafka`) и свою специфичную:

```yaml
build:
  project_name: nexus
  version: ${VERSION:dev}

logging:
  level: 4              # 2=error, 3=warn, 4=info, 5=debug
  output_in_file: false
  dir: logs
  debug: false

sentry:
  use: ${SENTRY_USE:false}
  dsn: ${SENTRY_DSN}                  # из .env
  environment: ${SENTRY_ENVIRONMENT:production}
  level: 2
  attach_stacktrace: true
  enable_tracing: true
  traces_sample_rate: 0.1

postgres:
  host: ${PG_HOST:localhost}
  port: ${PG_PORT:5432}
  database: nexus
  user: ${PG_USER:nexus}             # из .env
  password: ${PG_PASSWORD}             # из .env
  max_open_conns: 25                   # под 500 rps с Redis-кешем хватает с запасом
  max_idle_conns: 5
  conn_max_lifetime_min: 30

redis:
  host: ${REDIS_HOST:localhost}
  port: ${REDIS_PORT:6379}
  db: 0
  password: ${REDIS_PASSWORD}          # из .env, может быть пустым
  pool_size: 50                        # под 500 rps + LRU L2 cache
  min_idle_conns: 10
  dial_timeout_ms: 2000
  read_timeout_ms: 500                 # понижен — Redis должен быть быстрым
  write_timeout_ms: 500
  # TTL для разных типов ключей
  node_ttl_sec: 300                    # конфиг узла
  session_ttl_sec: 86400               # сессии UI (24 часа)
  ratelimit_window_sec: 60             # окно rate limit

clickhouse:
  # Начальные значения — потом перезаписываются из app_settings (PostgreSQL → Redis)
  host: ${CH_HOST:localhost}
  port: ${CH_PORT:9000}
  database: nexus_default
  user: ${CH_USER:default}             # из .env
  password: ${CH_PASSWORD}             # из .env
  batch_size: 500                      # под 500 rps — секунда трафика в батче
  flush_interval_sec: 5                # компромисс между актуальностью логов и нагрузкой на CH
  buffer_max_size: 100000              # ~100k записей в памяти, дальше — файл-фоллбек
  workers: 2                           # параллельных writer-горутин

kafka:
  brokers: ${KAFKA_BROKERS:localhost:9092}  # comma-separated, в проде 3+ broker'а
  async_topic: nexus.async
  dlq_topic: nexus.async.dlq
  consumer_group: nexus-sender
  # === Параметры топиков (применяются при автосоздании на старте) ===
  topic:
    partitions: 4                      # под параллелизм consumer'ов и rps
    replication_factor: 3              # в проде; для compose-окружения override → 1
    min_insync_replicas: 2             # acks=all требует подтверждения от 2 ISR
    retention_ms: 2592000000           # 30 дней = 30 * 24 * 60 * 60 * 1000
    retention_bytes: -1                # без лимита по объёму (-1 = unlimited)
    segment_ms: 86400000               # 1 день — ротация сегмента
    cleanup_policy: delete             # удалять старые сегменты по retention
    compression_type: lz4              # хороший баланс CPU/размер для JSON
    max_message_bytes: 10485760        # 10 MB (под крупные payload'ы)
  # === Producer (Receiver → Kafka для async) ===
  producer:
    acks: all                          # ждать подтверждения от всех ISR (надёжность)
    compression_type: lz4
    linger_ms: 5                       # микро-батчинг для throughput
    batch_size: 65536                  # 64 KB
    buffer_memory: 33554432            # 32 MB на producer
    retries: 2147483647                # max int — фактический лимит через delivery.timeout
    delivery_timeout_ms: 120000        # общий лимит на доставку с retry
    enable_idempotence: true           # exactly-once семантика
    max_in_flight_requests: 5
    request_timeout_ms: 30000
  # === Consumer (Sender ← Kafka) ===
  consumer:
    auto_offset_reset: earliest        # с начала, если нет закоммиченного offset
    enable_auto_commit: false          # коммитим вручную после успешной доставки
    fetch_min_bytes: 1
    fetch_max_bytes: 52428800          # 50 MB
    max_partition_fetch_bytes: 10485760  # 10 MB на партицию
    session_timeout_ms: 30000
    heartbeat_interval_ms: 10000
    max_poll_records: 500              # сколько сообщений за один poll
    max_poll_interval_ms: 300000       # 5 минут на обработку батча
    isolation_level: read_committed
    instances: 4                       # = partitions для максимального параллелизма

receiver:
  http_addr: :8080
  read_timeout_ms: 10000
  write_timeout_ms: 10000
  idle_timeout_sec: 120                # keep-alive
  max_body_bytes: 5242880              # 5 MB
  max_header_bytes: 1048576            # 1 MB
  rate_limit_per_node: 0               # 0 = без лимита
  # gRPC-клиент к Sender
  sender_grpc:
    addr: sender:9190
    pool_size: 8                       # пул gRPC-соединений
    timeout_ms: 30000
    keepalive_time_sec: 30
    keepalive_timeout_sec: 10

sender:
  grpc_addr: :9190
  grpc_max_concurrent_streams: 1000
  http_client:
    timeout_ms: 30000
    max_idle_conns: 100                # под 500 rps
    max_idle_conns_per_host: 20        # ключевое для keep-alive к узлам
    idle_conn_timeout_sec: 90
    dial_timeout_ms: 5000
    tls_handshake_timeout_ms: 5000
  workers: 50                          # пул воркеров для исходящих HTTP-вызовов

web:
  http_addr: :8000
  session_cookie_name: nexus_session # имя cookie с session-токеном
  session_cookie_secure: true          # cookie только по HTTPS (false для локальной разработки)
  session_cookie_samesite: strict      # strict / lax / none
  # session_ttl наследуется из redis.session_ttl_sec (см. §7.1)
  audit_retention_days: 365            # хранение записей user_audit (см. §7.13)
  replay_rate_limit_per_user_per_min: 10   # лимит на replay-запросы (см. §7.4.1)
  nodes_soft_limit: 10000              # предупреждение при достижении (см. §3.3)
  nodes_hard_limit: 50000              # отказ в создании (см. §3.3)
  api_token_rate_limit_per_min: 100    # лимит запросов с API-токеном (см. §7.14)
```

**Файл `.env` (рядом с `docker-compose.yml`).** Хранит все секреты и любые логины/пароли. Не коммитится; шаблон `.env.example` лежит в git:

```dotenv
# PostgreSQL
PG_USER=nexus
PG_PASSWORD=change_me_in_production

# Redis
REDIS_PASSWORD=change_me_in_production

# ClickHouse
CH_USER=default
CH_PASSWORD=change_me_in_production

# Kafka (если включена SASL — добавить KAFKA_SASL_USER/PASSWORD)

# Sentry
SENTRY_USE=true
SENTRY_DSN=https://xxx@sentry.io/yyy
SENTRY_ENVIRONMENT=production

# Шифрование чувствительных полей в БД (см. §5.5)
# 32 байта в base64. Сгенерировать: openssl rand -base64 32
ENCRYPTION_KEY=replace_me_with_base64_32_bytes

# Версия (подставляется в build.version)
VERSION=1.0.0
```

В production `.env` либо генерируется CI/CD из секрет-менеджера (Vault, AWS Secrets Manager, GitLab CI variables), либо монтируется в контейнер из защищённого хранилища.

### 8.4 Перезагрузка конфигурации

- Поля из секций `clickhouse` (часть, доступная через UI) и `sentry` дополнительно хранятся в таблице `app_settings` PostgreSQL (см. §14.5) и кешируются в Redis (ключ `app_settings`) — значения из БД накладываются поверх YAML на старте.
- Изменения настроек ClickHouse и Sentry через UI применяются без рестарта: запись в PostgreSQL → инвалидация ключа в Redis → graceful переинициализация соответствующих клиентов на всех инстансах сервиса (через pub/sub-канал Redis `nexus:config:reload`).
- Изменения, требующие рестарта (адреса PostgreSQL/Redis/Kafka, порты, размеры пулов), вносятся в YAML и применяются при перезапуске.
- Секреты (пароли БД, токены) **никогда не пишутся в YAML** — только в `.env` или внешнем секрет-менеджере.

