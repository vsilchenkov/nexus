# DEPLOYMENT.md — установка, эксплуатация, обновление и откат Nexus

Руководство по развёртыванию Nexus в продакшене: установка на чистом Linux-сервере,
запуск полностью в Docker и с внешними хранилищами, конфигурация адресов/логинов/паролей,
обновление, смена версии и откат. Запуск под Docker на Windows — в конце документа.

> Для **локальной разработки и отладки в VS Code** см. отдельный файл
> [DEVELOPMENT.md](./DEVELOPMENT.md). Этот документ — только про эксплуатацию.

---

## 1. Что разворачиваем

Nexus — три stateless Go-сервиса плюс набор хранилищ:

| Сервис     | Порт(ы)                         | Назначение                                            |
|------------|---------------------------------|-------------------------------------------------------|
| `web`      | `8000`                          | REST API `/api/*`, SPA-админка, Swagger UI, единый вход боевого трафика (`/api/v1/*` → проксируется в Receiver) |
| `receiver` | `8080`                          | Приём `/api/v1/request/*`, `/api/v1/requestAsync/*`, `/api/v1/callback/*` |
| `sender`   | `9190` (gRPC) + `9093→9091` (admin/health/metrics) | Доставка во внешние URL, запись логов в ClickHouse |

| Зависимость | Версия | Порт(ы)        | Для чего                                      |
|-------------|--------|----------------|-----------------------------------------------|
| PostgreSQL  | 16     | `5432`         | Конфиг узлов, пользователи, audit, настройки  |
| Redis       | 7      | `6379`         | Кеш узлов, сессии, rate-limit, circuit breaker |
| ClickHouse  | 24     | `8123`/`9000`  | Логи всех вызовов                             |
| Kafka       | 3.9 (KRaft) | `9092`     | Очередь async-запросов (`nexus.async`/`.dlq`/`.paused`) + durable-буфер проваленных CH-батчей (`nexus.logs.retry`, §38) |
| Prometheus  | 2.55   | `9091→9090`    | Scrape метрик сервисов **и источник дашбордов панели** (KPI/очередь/графики, §21) |

Сервисы — stateless: всё состояние в хранилищах. Поэтому обновление и откат сводятся
к замене бинарей/образов; данные остаются в PostgreSQL/ClickHouse/Kafka/Redis.

Целевая версия Go для сборки из исходников — **1.26**.

---

## 2. Конфигурация: где задавать адреса, логины и пароли

Конфигурация разнесена на два слоя:

- **`.env`** — секреты и адреса хранилищ (логины, пароли, хосты, порты, `ENCRYPTION_KEY`).
  В git не коммитится. Шаблон — [.env.example](./.env.example).
- **`config/config.yml`** — параметры приложения (таймауты, размеры пулов, лимиты).
  В git не коммитится; шаблон — [config/config.example.yml](./config/config.example.yml).
  Значения вида `${PG_HOST:postgres}` подставляются из `.env` (после `:` — дефолт).

> В Docker-образы зашит `config.example.yml` как `/app/config/config.yml` (см. Dockerfile'ы
> в [deploy/docker/](./deploy/docker/)). Поэтому **в Docker реальный конфиг отдельно готовить
> не нужно — достаточно `.env`**: все адреса/секреты подставятся в плейсхолдеры
> `${VAR:default}`. Файл `config/config.yml` нужен только при нативном запуске бинарей.

Приоритет выбора конфига бинарём: `--config <path>` → `$NEXUS_CONFIG` → `--debug`
(берёт `config_debug.yml`) → `config/config.yml`.

### Полная карта переменных `.env`

| Переменная            | Назначение                                              | Дефолт в шаблоне      |
|-----------------------|---------------------------------------------------------|-----------------------|
| `VERSION`             | Не используется (registry-путь убран, §9.2); версия приложения — из git (§9.0). Можно удалить | —        |
| `PG_HOST`             | Хост PostgreSQL                                         | `postgres`            |
| `PG_PORT`             | Порт PostgreSQL                                         | `5432`                |
| `PG_USER`             | Логин PostgreSQL                                        | `nexus`               |
| `PG_PASSWORD`         | Пароль PostgreSQL                                       | `change_me_in_production` |
| `REDIS_HOST`          | Хост Redis                                              | `redis`               |
| `REDIS_PORT`          | Порт Redis                                              | `6379`                |
| `REDIS_USER`          | ACL-юзер Redis (6+); пусто = default-юзер               | пусто                 |
| `REDIS_PASSWORD`      | Пароль Redis                                            | пусто                 |
| `CH_HOST`             | Хост ClickHouse                                         | `clickhouse`          |
| `CH_PORT`             | Нативный порт ClickHouse (TCP 9000)                     | `9000`                |
| `CH_USER`             | Логин ClickHouse                                        | `default`             |
| `CH_PASSWORD`         | Пароль ClickHouse                                       | пусто                 |
| `KAFKA_BROKERS`       | Список брокеров через запятую                           | `kafka:9092`          |
| `PROMETHEUS_URL`      | Адрес сервера Prometheus (query API) — источник дашбордов панели (§21). Пусто = метрики панели деградируют | `http://prometheus:9090` (в `.env.example`); в `config.example.yml` пусто |
| `PROMETHEUS_RETENTION`| Глубина хранения метрик Prometheus (`--storage.tsdb.retention.time` в compose). Определяет доступную историю графиков/KPI и объём тома `prometheus_data` | `90d` |
| `SENTRY_USE`          | Включить Sentry                                         | `false`               |
| `SENTRY_DSN`          | DSN Sentry                                              | пусто                 |
| `SENTRY_ENVIRONMENT`  | Окружение для Sentry                                    | `production`          |
| `ENCRYPTION_KEY`      | **Обязателен.** Ключ AES-256-GCM для шифрования кредов узлов в БД — 32 байта в base64 | заглушка |
| `NEXUS_RECEIVER_MAX_HOPS` | §32: лимит переходов запроса через шину (`X-Nexus-Hops`) до ответа 508 Loop Detected. `0` = дефолт 5; `<0` = защита от зацикливания выключена | `5` |

Дополнительно при single-broker Kafka (один узел) задавайте в `.env`
`KAFKA_TOPIC_REPLICATION_FACTOR=1` и `KAFKA_TOPIC_MIN_INSYNC_REPLICAS=1` — иначе создание
топиков упадёт с `InvalidReplicationFactor` (дефолты в `config.example.yml` рассчитаны
на кластер из 3+ брокеров: RF=3, ISR=2).

> **Внешний Kafka — лимит размера сообщения.** §38 durable-retry при недоступности
> ClickHouse шлёт проваленные батчи логов (с телами request/response, до неск. МБ) в
> топик `nexus.logs.retry`. Топики создаются с `max.message.bytes` из
> `kafka.topic.max_message_bytes` (по умолчанию **16 МиБ** — запас под
> `receiver.max_body_bytes` 10 МиБ × 1.33 base64 async-envelope, §68), но **брокерский**
> дефолт `message.max.bytes`/`replica.fetch.max.bytes` (~1 МиБ) перебивает
> per-topic-конфиг. В bundled-compose это уже выставлено (`KAFKA_MESSAGE_MAX_BYTES`/
> `KAFKA_REPLICA_FETCH_MAX_BYTES = 16777216`). При **внешнем** Kafka-кластере выставьте
> на брокерах `message.max.bytes` и `replica.fetch.max.bytes` **не ниже**
> `kafka.topic.max_message_bytes`, иначе крупные retry-батчи отвергаются
> (`Message Size Too Large`) и логи теряются.

> **Большие тела сквозь шину (§42).** Лимит размера одного gRPC-сообщения Receiver↔Sender
> задаётся `receiver.sender_grpc.max_message_bytes` и `sender.grpc_max_message_bytes` (оба
> дефолт **64 МиБ**). Прежний дефолт gRPC (4 МиБ) рвал большой ответ апстрима
> `ResourceExhausted desc = grpc: received message larger than max`. Значения **двух сервисов
> должны совпадать**; держите их `≥ receiver.max_body_bytes`. Для ответов крупнее 64 МиБ —
> поднимите оба параметра. Async-путь: producer публикует сообщение размером до
> `kafka.topic.max_message_bytes` (BatchBytes продьюсера авто-подтягивается до этого лимита) —
> убедитесь, что брокерский `message.max.bytes` его не перебивает (см. выше).

Переменные `VERSION` и `REGISTRY_BASE` больше не используются: registry-путь деплоя убран
(§9.2), деплой — сборкой из исходников на сервере (§9.1). Версия приложения берётся из git
при сборке (§9.0).

### Метрики панели и Prometheus (§21)

Дашборды Web-панели (KPI на Overview: входящие/исходящие/очередь/ошибки за 24ч; per-node
throughput; на странице узла — KPI, перцентили p95/p99 и график трафика) питаются из **единого
источника — Prometheus** (секция `prometheus.url`). Это адрес query API сервера Prometheus (напр.
`http://prometheus:9090`), **а не** `/metrics` самих сервисов. В Docker — задаётся через
`PROMETHEUS_URL` (см. таблицу). ClickHouse в метриках больше не участвует — он остаётся чисто
хранилищем логов (поиск/просмотр/replay). per-node KPI/график узла считаются по per-node счётчикам
Sender'а (`nexus_requests_total`, `nexus_request_incomplete_total`, гистограмма
`nexus_request_duration_seconds`), причём счётчики пишутся **независимо** от `logging_enabled` узла —
поэтому метрики узла видны, даже если логирование в ClickHouse у узла выключено.

**Глубина истории.** Доступный период графиков/KPI ограничен retention Prometheus — переменная
`PROMETHEUS_RETENTION` (по умолчанию `90d`). Чем больше глубина и кардинальность (число узлов ×
методов × бакетов гистограммы), тем больше объём тома `prometheus_data` — следите за его размером.

**Деградация без Prometheus.** Если `prometheus.url` пуст или сервер недоступен — панель не
падает: глобальные KPI, очередь Kafka, per-node throughput и per-node KPI/график узла отдают нули с
флагами `prometheus_available=false` / `chart_available=false` в ответе API. Поэтому для prod с
дашбордами Prometheus нужно поднять и указать его URL; без него вся остальная панель (узлы, логи,
настройки, audit) работает как прежде.

**§22 — Telegram-уведомления теперь требуют Prometheus.** Планировщик алертов считает ошибки узлов
из метрики Sender `nexus_request_incomplete_total` через Prometheus query API (единый источник с
графиками), а не из ClickHouse. Без настроенного `prometheus.url` уведомления **не запускаются**
(в лог пишется warning). Если используете Telegram-алерты — Prometheus обязателен. Метрика
`nexus_request_incomplete_total{method,node}` отдаётся на `/metrics` Sender'а и должна попадать в
scrape-конфиг.

### Мониторинг Kafka (§31)

Admin-only экран `/kafka` (раздел «Аудит») питается из **Prometheus** (throughput/lag/ошибки/top-узлы)
и **Kafka Admin API** (топики/брокеры/ping). Что нужно для прод-развёртывания:

- **Сетевой доступ Web → Kafka-брокеры.** Web Service теперь опционально подключается к брокерам
  (read-only metadata: Metadata/ListOffsets/ListGroups/OffsetFetch) по адресам `kafka.brokers`
  (`KAFKA_BROKERS`). Если доступа нет или `kafka.brokers` пуст — admin-клиент не создаётся, блоки
  «Топики»/«Брокеры»/«Проверить кластер» помечаются недоступными (`kafka_available=false`), остальной
  экран (KPI/графики из Prometheus) работает. Размер топика на диске не показывается (high-level
  клиент не отдаёт `DescribeLogDirs`).
- **Prometheus** тот же (`prometheus.url`). Async-трафик берётся из существующих
  `nexus_requests_total`/`nexus_request_incomplete_total` по `method="requestAsync"`. Дополнительно
  Receiver/Sender теперь экспонируют две новые серии на тех же `/metrics` (отдельный scrape не нужен):
  `nexus_kafka_in_flight{component="sender"}` (сообщения в обработке) и
  `nexus_kafka_produce_duration_seconds{topic}` (длительность публикации) — источник KPI «In-flight»
  и порога produce-latency.
- **Новые параметры конфига** (секция `web:`): `kafka_monitor_rate_limit_per_min` (дефолт 60 —
  лимит `/api/kafka/*` на пользователя) и `kafka_alerts_thresholds.*` (пороги health-banner/KPI,
  §31.5). Дефолты безопасны — задавать необязательно.
- **Доступ.** Раздел и все `/api/kafka/*` — только для роли `admin` (для остальных 403); метаданные
  кешируются в Redis (TTL 30с).

**§28 — Публичный адрес приложения (после публикации за доменом/reverse-proxy).** Полный адрес
узла, который показывает UI (`<origin>/api/v1/<verb>/<path>` + кнопка «Скопировать»), по умолчанию
берётся от адреса хоста в браузере. Когда приложение опубликовано под доменом (за reverse-proxy),
задайте публичный адрес в **Settings → Общие → Публичный адрес приложения** (например
`https://nexus.example.com`, только origin без пути и хвостового слеша). Значение хранится в
`app_settings` (БД, не env), правится admin'ом без рестарта; пустое — снова используется origin
браузера.

**§51 — Консоль служебных логов и runtime-уровень логирования.** Новый пункт сайдбара «Логи»
(admin): хвост slog-логов всех трёх сервисов + смена уровня логирования без рестарта. Для деплоя:

- **Миграций нет.** Уровень хранится в `app_settings.value → logging.level` (JSONB, 2=error..5=debug);
  не задан → используется `logging.level` из YAML каждого сервиса. Меняется через UI («Логи» →
  сегмент уровня) или `PUT /api/settings/app {"logging":{"level":N}}`; применяется во всех трёх
  сервисах через существующий Redis pub/sub (`nexus:config:reload`, секция `logging`).
- **Новые Redis-ключи** `nexus:logs:{receiver,sender,web}` — кольца последних ~2000 строк на сервис
  (LTRIM) с TTL 1 ч (логи мёртвого сервиса истекают). Память ~2–3 МБ суммарно; чистить вручную не
  нужно. Отдельный конфиг не требуется — используется общий Redis.
- Чувствительные значения атрибутов маскируются (`***`) ДО записи в Redis; лог-путь неблокирующий
  (при недоступном Redis строки дропаются, сервисы не тормозят).

**§52 — Трёхсостоянье статуса узла (OK/Degraded/Down).** Миграций и новой ENV нет. Для деплоя:

- **Метрика `nexus_node_last_request_error{node}` меняет значения**: было 0/1 («последний вызов
  ошибочен»), стало **0=ok (2xx), 1=degraded (ответил не-2xx <500), 2=down (транспортная ошибка
  или 5xx)**. Внешние алерты `>= 1` продолжают ловить любую проблему; порог «только down» — `>= 2`.
  Если на метрику завязаны дашборды с точным равенством `== 1` — обновите их.
- **Redis-ключ `nexus:node:last_error:<path>` меняет кодировку**: `"0"`=ok, `"1"`=down,
  `"2"`=degraded (кодировка НЕ совпадает с метрикой — намеренно, ради legacy-совместимости).
  Rolling-деплой безопасен: старые значения `"1"` новый Web читает как down (worst case,
  обновится следующим вызовом узла); старый Web при новом Sender degraded покажет как OK
  (транзиентно). Чистить ключи не нужно.
- API `GET /api/metrics/nodes`: новое поле `items[].last_outcome` (`ok|degraded|down`);
  `last_error` сохранено (back-compat, = `last_outcome != "ok"`).

### Генерация `ENCRYPTION_KEY`

Это 32 байта, закодированные в base64. Без валидного ключа сервисы не стартуют (exit 1).

```bash
# Linux / macOS
openssl rand -base64 32
```

Тонкие настройки приложения (таймауты, размеры пулов, лимиты узлов, параметры
ClickHouse-батчинга, cookie-флаги) живут в `config/config.example.yml` — правьте при
нативном запуске; в Docker они уже зашиты в образ и при необходимости переопределяются
монтированием своего `config.yml` (см. §7).

### Безопасность Web (Phase AUD.4)

- **Анти-брутфорс логина**: `web.login_rate_limit_per_min` (дефолт 10) — попыток
  `/api/auth/login` в минуту на IP и отдельно на login; превышение → 429 + запись в audit.
  `-1` отключает. При недоступном Redis лимит fail-open (вход не блокируется).
- **CSRF**: мутации `/api/*` под session-cookie дополнительно проверяются по заголовку
  `Origin` (несовпадение с Host → 403). Если публикуете Web за reverse-proxy, проследите,
  чтобы proxy не переписывал `Host` относительно того origin'а, с которого открыт UI
  (стандартная настройка `proxy_set_header Host $host` в nginx — корректна).
- **Security-заголовки**: Web выставляет CSP/nosniff/X-Frame-Options/Referrer-Policy
  автоматически; на `/swagger/*` CSP не ставится. SPA использует Google Fonts — CSP уже
  разрешает `fonts.googleapis.com`/`fonts.gstatic.com`; в полностью офлайн-контуре шрифты
  просто не загрузятся (graceful fallback на системные).
- `session_cookie_samesite: none` без `session_cookie_secure: true` теперь даёт warning
  при старте — такая комбинация отбрасывается браузерами.
- **Доверенные прокси (Phase AUD.5)**: `receiver.trusted_proxies` и `web.trusted_proxies` —
  CIDR/IP, чьим заголовкам `X-Forwarded-For` сервис верит при определении IP клиента
  (аудит, логи ClickHouse). Дефолт — loopback + приватные сети (RFC1918/ULA), что покрывает
  docker-compose. Если перед Nexus стоит внешний reverse-proxy с публичным адресом —
  перечислите его адрес явно, иначе IP клиента в логах будет адресом прокси.

---

## 3. Вариант A — полностью в Docker (рекомендуемый путь установки)

Поднимает **всё**: три сервиса Nexus + PostgreSQL + Redis + ClickHouse + Kafka + Prometheus
из одного файла [deploy/docker-compose.yml](./deploy/docker-compose.yml).

### 3.1. Подготовка чистого Linux-сервера

```bash
# Docker Engine + плагин compose (Ubuntu/Debian; для RHEL см. docs.docker.com)
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker "$USER"   # перелогиньтесь, чтобы группа применилась
docker compose version            # проверка: должен быть v2+
```

### 3.2. Развёртывание

```bash
# 1. Получить код на сервер (git clone по вашему remote, либо распаковать архив)
git clone <repo-url> nexus && cd nexus

# 2. Подготовить секреты
cp .env.example .env
#    Отредактируйте .env: PG_PASSWORD, REDIS_PASSWORD, CH_PASSWORD,
#    и обязательно ENCRYPTION_KEY (см. §2). Для single-broker Kafka добавьте
#    KAFKA_TOPIC_REPLICATION_FACTOR=1 и KAFKA_TOPIC_MIN_INSYNC_REPLICAS=1.

# 3. Собрать образы и поднять стек
docker compose -f deploy/docker-compose.yml up -d --build

# 4. Дождаться, пока зависимости станут healthy
docker compose -f deploy/docker-compose.yml ps
```

Миграции схемы PostgreSQL применяются **автоматически** при старте контейнеров `web` и
`receiver` (см. §8). Отдельный шаг не нужен.

### 3.3. Bootstrap пароля администратора

Миграция создаёт пользователя `admin` с пустым (NULL) паролем — войти нельзя, пока пароль
не задан:

```bash
docker compose -f deploy/docker-compose.yml run --rm web --set-admin-password 'СильныйПароль'
```

Команда подключится к PostgreSQL, установит пароль и завершится.

### 3.4. Проверка

```bash
curl http://localhost:8000/health          # web
curl http://localhost:8080/health          # receiver
curl http://localhost:9093/health          # sender (admin-порт)

curl -X POST http://localhost:8000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"login":"admin","password":"СильныйПароль"}'
```

- UI: `http://<сервер>:8000/`
- Swagger UI (§25): Web API — `http://<сервер>:8000/swagger/web/index.html`,
  Receiver API — `http://<сервер>:8000/swagger/receiver/index.html`
  (старый `/swagger/index.html` редиректит на web). Оба дока раздаёт Web-бинарь.

### 3.5. Управление стеком

```bash
docker compose -f deploy/docker-compose.yml logs -f web receiver sender   # логи
docker compose -f deploy/docker-compose.yml stop                          # остановить
docker compose -f deploy/docker-compose.yml down                          # снести контейнеры (данные в volume сохранятся)
docker compose -f deploy/docker-compose.yml down -v                       # снести вместе с данными (ОПАСНО)
```

---

## 4. Вариант B — Docker + внешние сервисы (PostgreSQL/ClickHouse/Kafka/Redis)

Когда хранилища уже развёрнуты отдельно (управляемый PostgreSQL, кластер ClickHouse,
общий Kafka и т.д.) — поднимаем только три сервиса Nexus через
[deploy/docker-compose.app.yml](./deploy/docker-compose.app.yml) (бундл-зависимости не запускаются).

### 4.1. Где указывать адреса/логины/пароли

Всё — в `.env`. Замените дефолтные хосты на адреса ваших сервисов:

```dotenv
# PostgreSQL
PG_HOST=pg.internal.example.com
PG_PORT=5432
PG_USER=nexus
PG_PASSWORD=<пароль>

# Redis
REDIS_HOST=redis.internal.example.com
REDIS_PORT=6379
REDIS_USER=               # если включён ACL — укажите юзера
REDIS_PASSWORD=<пароль>

# ClickHouse
CH_HOST=ch.internal.example.com
CH_PORT=9000             # нативный TCP-протокол
CH_USER=nexus
CH_PASSWORD=<пароль>

# Kafka (через запятую для нескольких брокеров)
KAFKA_BROKERS=kafka1.internal:9092,kafka2.internal:9092,kafka3.internal:9092

# Обязательно
ENCRYPTION_KEY=<base64 32 байта>
```

Если внешние сервисы крутятся **на самом docker-хосте**, используйте `host.docker.internal`
(в `docker-compose.app.yml` он уже проброшен через `extra_hosts`):

```dotenv
PG_HOST=host.docker.internal
CH_HOST=host.docker.internal
REDIS_HOST=host.docker.internal
KAFKA_BROKERS=host.docker.internal:9092
```

### 4.2. Запуск

```bash
docker compose -f deploy/docker-compose.app.yml up -d --build

# bootstrap пароля admin (как в §3.3, но с app-compose)
docker compose -f deploy/docker-compose.app.yml run --rm web --set-admin-password 'СильныйПароль'

docker compose -f deploy/docker-compose.app.yml ps
docker compose -f deploy/docker-compose.app.yml logs -f web receiver sender
```

### 4.3. Предусловия на стороне внешних сервисов

- **PostgreSQL**: должна существовать БД `nexus`, доступная под `PG_USER`/`PG_PASSWORD`.
  Таблицы создадутся автомиграцией (§8). У роли нужны права `CREATE` в этой БД.
- **ClickHouse**: пользователь должен иметь право создавать БД и таблицы — Nexus заводит
  по БД на команду (`nexus_<slug>`, для default — `nexus_default`) и создаёт таблицы логов.
- **Kafka**: автосоздание топиков на брокере должно быть **разрешено**, либо заранее
  создайте `nexus.async`, `nexus.async.dlq`, `nexus.async.paused` и `nexus.logs.retry` (Nexus сам пытается их завести с
  `retention.ms=7 дней` + `retention.bytes=40 ГиБ` **на партицию**; на single-broker не забудьте
  RF=1/ISR=1 — см. §2). Топик `nexus.logs.retry`
  (§38) — durable-буфер проваленных CH-батчей при недоступности ClickHouse; его retention должен
  покрывать максимально ожидаемый простой CH × объём логов (иначе при очень долгом простое старые
  батчи истекут по retention и не доедут в CH). Имя настраивается `kafka.retry_topic`; пустое
  значение полностью выключает retry (батчи теряются при сбое CH).

  Топик `nexus.async.paused` (§3.6, имя — `kafka.paused_topic`) — delay-очередь сообщений узлов,
  стоящих на паузе: они переносятся туда, чтобы не задерживать другие узлы в той же партиции
  основного топика. Обслуживается sweeper'ом Sender'а (`sender.paused_sweep`: `disabled`,
  `interval_sec` — период прохода и максимальная задержка доставки после снятия паузы,
  `max_scan`). **Выключение sweeper'а означает, что накопленный за паузу бэклог не будет доставлен
  вообще**, а его retention должен покрывать максимально ожидаемую длительность паузы. Контроль —
  метрика `nexus_kafka_lag` по consumer-group `<kafka.consumer_group>-paused`: не убывающий после
  снятия пауз lag означает, что sweeper не работает.

  > **Смена retention/размера на УЖЕ существующем топике.** Nexus применяет `kafka.topic.*` из
  > `config.yml` **только при создании топика** (`CreateTopics`); `AlterConfigs` в коде нет, поэтому
  > правка `config.yml` **не меняет живой топик**. Чтобы изменить retention или лимит размера
  > сообщения на работающем брокере **без пересоздания и без простоя** — примените конфиг напрямую
  > (изменение мгновенное, данные и оффсеты сохраняются):
  >
  > ```bash
  > # retention.bytes — ПЕР-ПАРТИЦИЯ (суммарно по топику = значение × partitions)
  > kafka-configs.sh --bootstrap-server <broker>:9092 --entity-type topics \
  >   --entity-name nexus.async --alter \
  >   --add-config retention.ms=604800000,retention.bytes=42949672960
  > # лимит размера сообщения (например, после поднятия receiver.max_body_bytes):
  > kafka-configs.sh --bootstrap-server <broker>:9092 --entity-type topics \
  >   --entity-name nexus.async --alter --add-config max.message.bytes=16777216
  > # проверить: --describe вместо --alter/--add-config
  > ```
  >
  > В Docker: `docker exec <kafka-контейнер> /opt/kafka/bin/kafka-configs.sh ...` (образ
  > `apache/kafka` — путь `/opt/kafka/bin`). Повторите для `nexus.async.dlq`, `nexus.async.paused`
  > и `nexus.logs.retry`, чтобы все топики совпадали с `config.yml`. Альтернатива (с кратким
  > простоем) — обновить `KAFKA_LOG_RETENTION_*` в compose и `docker compose up -d kafka`, но
  > брокерный дефолт бьёт **только по новым** топикам; существующие всё равно правятся
  > `kafka-configs --alter`.
- **Redis**: при включённом ACL задайте `REDIS_USER` и `REDIS_PASSWORD`.

---

## 5. Вариант C — Docker, внешний только ClickHouse (PG/Redis/Kafka — в Docker) — основной прод-путь

Когда есть отдельный (управляемый или кластерный) ClickHouse, а PostgreSQL, Redis и Kafka
удобнее держать рядом в Docker. Это **рекомендуемый прод-вариант**, поэтому его compose лежит
в корне проекта — [docker-compose.yml](./docker-compose.yml) — и запускается стандартной
командой `docker compose up -d` **без** `-f` (не нужно помнить, какой файл прод). Поднимает
bundled PostgreSQL + Redis + Kafka + Prometheus + три сервиса Nexus, **без** контейнера ClickHouse.

> Корневой `docker-compose.yml` уже монтирует `config/config.yml` поверх зашитого в образ
> `/app/config/config.yml` для всех трёх сервисов (см. §7) — отдельный override для конфига не
> нужен. Перед первым запуском создайте `config/config.yml` из
> [config/config.example.yml](./config/config.example.yml) (если правите дефолты; плейсхолдеры
> `${VAR}` в нём берутся из `.env`).

### 5.1. Где указывать адрес/логин/пароль ClickHouse

В `.env` задаются только параметры внешнего ClickHouse; PG/Redis/Kafka остаются на дефолтных
именах контейнеров — их хосты менять не нужно:

```dotenv
# Внешний ClickHouse
CH_HOST=ch.internal.example.com
CH_PORT=9000              # нативный TCP-протокол
CH_USER=nexus
CH_PASSWORD=<пароль>

# Bundled PG/Redis — задайте только пароли (хосты по умолчанию: postgres / redis):
PG_PASSWORD=<пароль>
REDIS_PASSWORD=<пароль>
# KAFKA_BROKERS=kafka:9092, PG_HOST=postgres, REDIS_HOST=redis — НЕ меняем

# Обязательно
ENCRYPTION_KEY=<base64 32 байта>
```

Если внешний ClickHouse крутится на самом docker-хосте — укажите `CH_HOST=host.docker.internal`
(проброшен в compose через `extra_hosts`).

### 5.2. Запуск

Из корня проекта, стандартной командой (compose сам берёт корневой `docker-compose.yml`):

```bash
docker compose up -d --build

# bootstrap пароля admin:
docker compose run --rm web --set-admin-password 'СильныйПароль'

docker compose ps
docker compose logs -f web receiver sender
```

Миграции PostgreSQL применяются автоматически (§8) — БД `nexus` и Redis/Kafka подняты в этом
же стеке, готовить их вручную не нужно.

### 5.3. Предусловия на стороне ClickHouse

Только для ClickHouse (как в §4.3): CH-юзер должен иметь право создавать БД и таблицы — Nexus
заводит БД на команду (`nexus_<slug>`, для default — `nexus_default`) и создаёт таблицы логов.
PostgreSQL/Redis/Kafka — bundled, как в Варианте A.

---

## 5А. Вторая нода Nexus на том же ClickHouse (§70)

**Нода** — самостоятельное развёртывание шины со своими PostgreSQL, Redis и Kafka, которое пишет
логи в **общий** с другими нодами ClickHouse. Действующая нода при этом не меняется: у неё пустой
`instance.id`, её базы остаются `nexus_<slug>`, поведение прежнее.

### 5А.1. Что задать в конфиге новой ноды

```yaml
instance:
  id: kz                # 1..8 символов ^[a-z][a-z0-9]{0,7}$; БД станут nexus_kz_<slug>
  adopt_unowned: false  # аварийный флаг, см. 5А.5
```

или через `.env`: `NEXUS_INSTANCE_ID=kz`.

**Идентификатор неизменяем после первого запуска.** Он фиксируется в PostgreSQL этой ноды
(`instance_identity`), и при расхождении с конфигом сервис не стартует: переименование
идентификатора не переименовывает уже созданные базы ClickHouse.

### 5А.2. Обязательные требования

| Ресурс | Требование | Почему |
|---|---|---|
| PostgreSQL | свой | вся конфигурация ноды, включая имена её БД ClickHouse |
| Redis | **свой сервер**, не другая база того же | pub/sub (`nexus:config:reload`, `nexus:nodes:invalidate`) не изолируется номером базы: перезагрузка настроек одной ноды применилась бы к другой |
| Kafka | свой кластер (либо переопределить ВСЕ имена топиков и `consumer_group`) | имена по умолчанию совпадают, и Sender одной ноды читал бы сообщения другой |
| Prometheus | свой | запросы панели метрик агрегируют без селектора ноды — на общем Prometheus дашборд покажет суммарный трафик обеих |
| ClickHouse | общий (в этом весь смысл) | разведение — суффиксом в именах БД + маркер владения |

Рекомендуется (не обязательно) отдельный пользователь ClickHouse на ноду:

```sql
CREATE USER nexus_kz IDENTIFIED BY '<пароль>';
GRANT ALL ON nexus_kz_%.* TO nexus_kz;
GRANT SELECT ON system.tables  TO nexus_kz;
GRANT SELECT ON system.parts   TO nexus_kz;
GRANT SELECT ON system.columns TO nexus_kz;
GRANT SELECT ON system.databases TO nexus_kz;
GRANT SELECT ON system.data_skipping_indices TO nexus_kz;
```

Это физическая страховка поверх программного гейта: даже при ошибке в конфигурации нода не сможет
изменить чужие базы.

### 5А.3. Что происходит при первом запуске

1. Миграции создают `instance_identity`; первый стартовавший сервис записывает туда `instance.id`.
2. Web переименовывает базу сидированной команды `default` на `nexus_kz_default` (только на первом
   запуске и только пока в базе нет узлов).
3. Web захватывает свои базы: в каждой создаётся служебная таблица-маркер `__nexus_owner` с кодом
   ноды. Она невидима в разделе «Бесхозные таблицы» и **удалять её нельзя**.
4. Дальше все разрушающие операции (очистка по retention, удаление бесхозных таблиц, ALTER'ы
   синхронизации схемы, переименование и создание таблиц, очистка неудачных доставок) выполняются
   только над базами со своим маркером.

**Если новую ноду запустить с пустым `instance.id`, а в ClickHouse уже есть `nexus_default`,
Web не стартует** (процесс завершается с кодом 1) и сообщит, что нужно задать `instance.id`. Это
защита от сценария, в котором вторая нода начинает писать в базы первой.

Гейт срабатывает только на действительно новой ноде — когда выполняются **оба** условия: нода ещё
ни разу не захватывала базы (`instance_identity.ch_claimed = false`) и в её PostgreSQL нет ни узлов,
ни созданных команд. Поэтому:

- обновление **действующей** ноды до этой версии проходит штатно: она усыновляет свои базы;
- перезапуск новой ноды сразу после развёртывания (узлов ещё нет) тоже проходит: база уже помечена
  её маркером.

### 5А.4. Проверка после запуска

```bash
# 1. Базы разведены
clickhouse-client -q "SHOW DATABASES LIKE 'nexus%'"

# 2. Маркеры владения проставлены
clickhouse-client -q "SELECT * FROM nexus_kz_default.__nexus_owner"

# 3. В интерфейсе новой ноды в шапке виден чип KZ, а в диалоге создания команды
#    предпросмотр имени базы показывает nexus_kz_<slug>.
```

### 5А.5. Аварийные флаги

| Флаг | Что разрешает | Когда нужен |
|---|---|---|
| `--ch-adopt` (или `instance.adopt_unowned: true`) | присвоить существующие базы **без** маркера, включая случай «свежая PostgreSQL + база уже есть» | PostgreSQL пересоздали или восстановили из бэкапа, а ClickHouse остался |
| `--instance-id-force` | однократно переписать сохранённый `instance.id` | осознанная смена кода ноды; **базы в ClickHouse при этом не переименовываются** — это делается вручную |

Чужой маркер не перебивается ни одним флагом: базу с идентификатором другой ноды нода не тронет
никогда.

**Смена `instance.id` на работающей ноде — полная процедура.** Одного `--instance-id-force`
недостаточно: маркер внутри уже созданных баз хранит ПРЕЖНИЙ идентификатор, и после смены нода
увидит собственные базы как чужие и не поднимется. Порядок такой:

```sql
-- 1. Остановить сервисы ноды. Переименовать базы в ClickHouse:
RENAME DATABASE nexus_default TO nexus_kz_default;   -- и так для каждой команды

-- 2. Удалить маркеры владения — их перезапишет нода при следующем старте:
DROP TABLE nexus_kz_default.__nexus_owner;
```

```sql
-- 3. В PostgreSQL ноды поправить имена баз команд:
UPDATE teams SET ch_database = 'nexus_kz_' || slug WHERE ch_database = 'nexus_' || slug;
UPDATE nodes SET clickhouse_table = 'nexus_kz_' || substring(clickhouse_table from 'nexus_(.*)')
WHERE clickhouse_table LIKE 'nexus_%' AND NOT external_table;
```

4. Запустить Web один раз с `--instance-id-force` (и с `--ch-adopt`, если в PostgreSQL ещё нет
   узлов) — нода перезапишет маркеры под новый идентификатор.

Проверьте результат до запуска остальных сервисов: `SELECT * FROM <db>.__nexus_owner` должен
показать новый идентификатор.

> **Важно.** Гейт защищает начиная с версии, в которой появился §70. Вторая нода, поднятая на более
> старой сборке с пустым `instance.id`, по-прежнему затрёт данные первой.

---

## 6. Вариант D — нативные бинари + systemd (без Docker)

Альтернатива для серверов без Docker. Бинари stateless, хранилища — внешние.

```bash
# Сборка под Linux (на любой машине с Go 1.26):
make build-linux            # → bin/receiver, bin/sender, bin/web

# или поштучно при нехватке памяти линкера:
go build -ldflags="-s -w" -o bin/web ./cmd/web
```

Скопируйте на сервер `bin/*`, каталог `migrations/`, `config/config.yml` (на основе
`config.example.yml`, с реальными адресами или плейсхолдерами из окружения).

Пример unit'а systemd (`/etc/systemd/system/nexus-web.service`):

```ini
[Unit]
Description=Nexus Web Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nexus
WorkingDirectory=/opt/nexus
EnvironmentFile=/opt/nexus/.env
ExecStart=/opt/nexus/bin/web --config /opt/nexus/config/config.yml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Аналогичные unit'ы для `nexus-receiver` и `nexus-sender` (поменяйте `Description` и путь
к бинарю). Затем:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now nexus-web nexus-receiver nexus-sender
```

`migrations/` должен лежать рядом с рабочим каталогом (`WorkingDirectory`), чтобы автомиграция
их нашла. Bootstrap пароля: `/opt/nexus/bin/web --config /opt/nexus/config/config.yml --set-admin-password '...'`.

---

## 7. Переопределение `config.yml` в Docker

Параметры приложения (пулы, таймауты, лимиты) можно менять без пересборки образа — монтированием
своего `config/config.yml` поверх зашитого в образ `/app/config/config.yml`.

**Корневой прод-`docker-compose.yml` (Вариант C, §5) делает это уже из коробки** — у `web`,
`receiver` и `sender` прописан `- ./config/config.yml:/app/config/config.yml:ro`. Достаточно
держать актуальный `config/config.yml` в корне проекта и перезапустить сервисы. Плейсхолдеры
`${VAR}` в смонтированном файле по-прежнему подставляются из `.env`.

Для прочих вариантов установки (`deploy/docker-compose.yml` — Вариант A, `deploy/docker-compose.app.yml`
— Вариант B), где монтирования нет, создайте `deploy/docker-compose.override.yml`:

```yaml
services:
  web:
    volumes:
      - ../config/config.yml:/app/config/config.yml:ro
  receiver:
    volumes:
      - ../config/config.yml:/app/config/config.yml:ro
  sender:
    volumes:
      - ../config/config.yml:/app/config/config.yml:ro
```

Compose автоматически подхватывает `docker-compose.override.yml`, лежащий рядом с базовым файлом,
либо укажите его явно через `-f`.

---

## 8. Миграции схемы

Миграции PostgreSQL — в каталоге [migrations/](./migrations/) (`NNNN_*.up.sql` / `.down.sql`,
инструмент — `golang-migrate`). Применение защищено advisory-lock'ом, поэтому одновременный
старт нескольких сервисов безопасен.

- **Автоматически на старте** применяют `web` и `receiver` (`sender` — нет). Во всех
  Docker-вариантах (§3, §4, §5) отдельный шаг миграций не нужен.
- **§22:** миграция `0010_node_logging_controls` добавляет в `nodes` колонки `logging_enabled`
  (DEFAULT true), `max_body_size_enabled` (DEFAULT false), `max_body_size` (DEFAULT 0). Применяется
  тем же автомиграционным путём; для существующих узлов логирование остаётся включённым, лимита
  тела нет (поведение не меняется).
- **§23:** миграция `0011_host_allowlist` добавляет каталог разрешённых хостов (`host_allowlist`),
  связь `node_allowed_hosts` (M2M) и trigger денормализации `usage_count`. Существующее поле
  `nodes.url_allowed_hosts TEXT[]` сохраняется как денормализованный снимок — Receiver не трогается,
  поведение существующих узлов не меняется. Каталог изначально пуст.
- **§24:** миграция `0012_headers_catalog` добавляет справочник HTTP-заголовков (`headers_catalog`,
  UNIQUE по `lower(name)`). Привязка к узлу остаётся в `nodes.forward_headers TEXT[]` — Receiver не
  трогается; usage_count считается on-read. Каталог изначально пуст.
- **§26:** миграция `0013_user_role_manager` расширяет CHECK-constraint `users_role_check` до
  `('admin','manager','viewer')` — добавляет роль `manager`. Новых колонок/таблиц нет, существующие
  пользователи не затрагиваются. Откат (`down`) переводит существующих менеджеров в `viewer` перед
  возвратом старого constraint.
- **§27:** миграция `0014_rmq_async_node` добавляет метод `RabbitMQAsync` в справочник `methods` и
  nullable-колонки `rmq_*`/`pull_*` в `nodes` + constraint `chk_rmq_fields`. Для request/requestAsync
  колонки NULL — поведение не меняется. Откат (`down`) сперва удаляет узлы `RabbitMQAsync`, затем
  колонки и значение метода. **Зависимость:** появился клиент `github.com/rabbitmq/amqp091-go`
  (vendored в go.mod) — внешний RabbitMQ-брокер Nexus НЕ поднимает, он только подключается к уже
  существующей очереди, указанной в узле. **Конфиг:** новая секция `receiver.puller`
  (`disabled` — по умолчанию false, `reconcile_sec` — 15) — Puller-воркеры живут в Receiver; при
  отсутствии узлов RabbitMQAsync это no-op. Health-снимки воркеров пишутся в Redis-hash `rmq:health`
  (Web читает для UI) — дополнительной инфраструктуры не требуют.
- **§36 — авто-репроцессор DLQ.** Миграции `0017_node_dlq_ttl` и `0018_node_dlq_retry_delay` добавляют в
  `nodes` колонки `dlq_ttl_seconds` (`NOT NULL DEFAULT 86400` = 24 ч) и `dlq_retry_delay_seconds`
  (`NOT NULL DEFAULT 300` = 5 мин). Аддитивные: `NOT NULL DEFAULT` атомарно заполняет существующие узлы
  дефолтами — отдельный backfill не нужен, поведение существующих узлов не меняется. Откат (`down`) просто
  удаляет колонки. **Новый конфиг:** опциональная секция `sender.reprocessor` (`disabled` false→включён,
  `interval_sec` 300, `max_scan` 1000) — есть дефолты, при отсутствии секции репроцессор работает с ними.
  Отдельной инфраструктуры/ENV не требует: sweeper читает существующий `nexus.async.dlq` отдельной
  consumer-группой `<consumer_group>-dlq-reprocess`.
- **§39 — path-passthrough + лог-колонки.** Миграция `0019_node_path_passthrough` добавляет в `nodes`
  колонку `path_passthrough` (`BOOLEAN NOT NULL DEFAULT false`) — аддитивно, поведение существующих узлов
  не меняется (passthrough по умолчанию выключен), откат удаляет колонку. **ClickHouse:** в лог-таблицы
  узлов добавляется колонка `http_method` — на старте `web` и `receiver` автоматически выполняется
  идемпотентный `ALTER TABLE … ADD COLUMN IF NOT EXISTS http_method String DEFAULT '' AFTER type`
  (`EnsureHTTPMethodColumn`, по образцу §37 `node_id`); отдельного шага не требуется, новые таблицы
  получают колонку из шаблона. Колонка `method` существующих таблиц переосмыслена (теперь подпуть
  passthrough вместо HTTP-глагола) **без миграции данных** — старые записи остаются как есть, новый
  трафик пишет по новой семантике (глагол ушёл в `http_method`). Новой ENV/инфраструктуры нет.
- **§42.10 — истинные размеры тел в логах.** Postgres-миграций нет. **ClickHouse:** в лог-таблицы
  узлов добавляются колонки `request_size Int64 DEFAULT 0` и `response_size Int64 DEFAULT 0` — на
  старте `web` и `sender` автоматически выполняется идемпотентный `ALTER … ADD COLUMN IF NOT EXISTS`
  (`EnsureBodySizeColumns`, по образцу §37/§39), затем **разовый backfill исторических строк**
  (`BackfillBodySizes`): мутации `ALTER … UPDATE size = length(col)` заполняют размеры байтовой
  длиной сохранённой (возможно усечённой) копии тела. Мутации асинхронные и идемпотентные;
  guard-`count()` пропускает уже заполненные таблицы, так что повторные старты мутаций не плодят;
  след миграции — Info-лог `backfill body sizes: scheduling mutations` с числом строк. **Порядок
  деплоя не важен:** старый Sender пишет явным списком колонок (новые получают DEFAULT 0, затем
  их добьёт backfill при следующем старте нового бинаря); новый Sender/Web сами выполняют Ensure до
  первых INSERT/SELECT; записи из Kafka `nexus.logs.retry`, созданные старым бинарём, десериализуются
  с нулевыми размерами. Отдельного шага/ENV/инфраструктуры не требуется.

  > **Исправлено в 1.13.2 (было в 1.12.0–1.13.1).** Утверждение «новый Web сам выполняет Ensure»
  > до 1.13.2 было верно **только для команды `default`**: Web собирал список таблиц с фильтром
  > `List(TeamID: defaultTeamID)` и БД остальных команд (`nexus_<slug>.*`) не альтерил вовсе. На
  > мультикомандной установке таблицы не-default команд мигрировал только Sender, поэтому чтение
  > логов таких узлов падало с CH `code 47 Unknown expression identifier request_size` в окне
  > «новый Web уже поднят, новый Sender ещё нет» (боевой инцидент, Sentry 158619). С 1.13.2 Web
  > берёт таблицы через беcфильтровый `ListClickHouseTables` — симметрично Sender'у, порядок
  > деплоя действительно не важен. Если разворачиваете 1.12.0–1.13.1 на несколько команд —
  > убедитесь, что Sender рестартовал после Web.
- **§67 — client_host (reverse-DNS имя клиента) в логах.** Postgres-миграций нет. **ClickHouse:**
  в лог-таблицы узлов добавляется колонка `client_host String DEFAULT ''` **AFTER IP** — на старте
  `web` и `sender` автоматически выполняется идемпотентный `ALTER … ADD COLUMN IF NOT EXISTS`
  (`EnsureClientHostColumn`, по образцу §39 `http_method`); новые таблицы получают колонку из
  шаблона. **Порядок деплоя не важен** (Ensure в обоих сервисах). Новая секция конфига
  `sender.rdns` (`disabled`/`timeout_ms`/`cache_ttl_sec`/`negative_ttl_sec`, включена по
  умолчанию, см. [config/config.example.yml](config/config.example.yml)); кеш живёт в
  существующем Redis (`nexus:rdns:*`), новой инфраструктуры нет. Резолв требует **PTR-записей в
  корпоративном DNS** для клиентских подсетей и работающего DNS из контейнера Sender'а
  (проверка: `docker exec <sender> getent hosts 192.168.86.246`) — иначе колонка молча остаётся
  пустой (fail-open, это штатно).

  > **⚠ Внешние (ручные) таблицы §64 Nexus НЕ альтерит** — их владельцы выполняют вручную
  > (SQL включается в описание релиза, см. правило в §9.5-A):
  >
  > ```sql
  > ALTER TABLE <db>.<table> ADD COLUMN IF NOT EXISTS client_host String DEFAULT '' AFTER IP
  > ```
  >
  > До ALTER: чтение логов такого узла падает, новые записи буферизуются в `nexus.logs.retry`
  > (§38) и доигрываются после ALTER (потери нет); `POST /api/ch-tables/verify` показывает
  > `Missing: client_host`.
- **§55 — dry-run с реальным вызовом target.** Миграций (PG и CH) нет. Появляется **новая
  зависимость `Web → Sender` по gRPC** и новая секция конфига `web.sender_grpc` (по образцу
  `receiver.sender_grpc`, см. [config/config.example.yml](config/config.example.yml)):

  ```yaml
  web:
    sender_grpc:
      addr: sender:9190   # пусто/не задано → реальный режим dry-run выключен
      pool_size: 2
      timeout_ms: 30000   # должен покрывать timeout_ms узла + ретраи
  ```

  Секция **опциональна**: `addr` намеренно **без дефолта** — если её не добавить, поведение не
  меняется (mock-режим работает как раньше, шаг «Response» реального режима вернёт `skipped` с
  внятной причиной). Web не падает, если Sender недоступен: ошибка создания клиента логируется как
  `warn`, а не эскалируется — инструмент отладки не должен ронять сервис. В Docker-варианте адрес
  резолвится по имени сервиса (`sender:9190`), в варианте D (нативные бинари) — `127.0.0.1:9190`.
  Порт 9190 у Sender уже слушается для Receiver'а, открывать новый не нужно — но Web должен иметь до
  него сетевой доступ (в варианте B/C проверьте firewall/сеть между контейнерами).

  > **Порядок деплоя важен: сначала `sender`, потом `web`.** Web просит Sender выполнить тестовый
  > вызов с флагом `dry_run=true` — по нему Sender гасит побочку: запись в ClickHouse, метрики §6,
  > статус узла (§41/§46/§52) и **circuit breaker** (§9.5/§50.4). **Старый Sender новое поле
  > проигнорирует**, и тестовый запрос будет учтён как боевой: неудачный тест покрасит живой узел в
  > Down и может **открыть его breaker → 503 реальному трафику**. Поэтому в окне «новый Web + старый
  > Sender» реальным режимом dry-run пользоваться нельзя. Обратный порядок (новый Sender + старый
  > Web) безопасен: старый Web поле не шлёт, Sender ведёт себя как раньше.

- **§40 — HTTP-метод «Любой» (ANY).** Миграция `0020_node_method_any` пересоздаёт CHECK-констрейнты
  `nodes_incoming_method_check`/`nodes_outgoing_method_check` с добавлением значения `'ANY'` (аддитивно;
  применяется автоматически на старте, дефолт колонок — POST). Откат (`down`) вернёт CHECK без `'ANY'` —
  упадёт, если остались узлы со значением `ANY` (привести к конкретному методу до отката). Новой
  ENV/инфраструктуры нет.
- **§41 — универсальная динамическая авторизация + каталог полей запроса.** Две миграции (применяются
  автоматически на старте): `0021_incoming_auth_dynamic` добавляет в `nodes` колонки
  `incoming_auth_dynamic_source` (CHECK `header|query`, дефолт `header`) и `incoming_auth_dynamic_field`
  (дефолт `Authorization`) — существующие узлы ведут себя как раньше; **плюс data-fix**: пинит
  существующие `basic_from_request` на `header`/`Authorization` (обязательно — иначе после выката
  source/field-aware код читал бы query `token` вместо заголовка `Authorization` и сломал бы проброс).
  `0022_request_fields_catalog` создаёт справочник `request_fields_catalog`. Новой ENV/инфраструктуры
  нет; метрика `nexus_node_last_request_error{node}` (Sender) появляется автоматически. Откат (`down`)
  удаляет колонки/таблицу (для `0021` basic_from_request-узлы остаются на `header`/`Authorization` —
  безопасно).
- **§49 — избранные команды.** Миграция `0023_user_team_favorites` создаёт таблицу
  `user_team_favorites(user_id, team_id, position)` с составным FK на `user_teams` ON DELETE CASCADE
  (применяется автоматически на старте; чисто аддитивная, существующие данные не трогает). Новой
  ENV/инфраструктуры нет. Откат (`down`) удаляет таблицу — теряется только персональное избранное.
- **Таймаут узла до 600с.** Миграция `0024_nodes_timeout_600s` пересоздаёт CHECK
  `nodes_timeout_range` (максимум `timeout_ms` 300000 → 600000 мс; применяется автоматически на
  старте, данные не трогает). Откат (`down`) ужимает значения >300000 до 300000 — при откате
  завышенные таймауты узлов теряются. **Важно при деплое:** проверить боевой
  `receiver.write_timeout_ms` — он должен превышать максимальный `timeout_ms` узла (рекомендуется
  610000), иначе долгие sync-запросы будут рваться самим Receiver'ом (клиент получит 502 от
  Web-прокси). `sender.http_client.timeout_ms` больше не капает per-node таймаут — это только
  fallback для запросов без него.
- **§66 — отображаемое имя пользователя.** Миграция `0028_user_name` (применяется автоматически на
  старте) добавляет в `users` колонку `name` (`VARCHAR(255) NOT NULL DEFAULT ''`) и **разово
  проставляет `name = login`** существующим пользователям — администратор потом правит имена руками
  (Settings → Users). UI везде показывает имя вместо логина; снапшоты автора (`nodes.created_by/
  updated_by`, `user_audit.user_login`) с этого релиза пишутся именем — старые записи остаются с
  логином (он равен имени на момент миграции). Живые Redis-сессии, созданные до выката, поля `name`
  не несут — до следующего входа подписи фолбэчат на логин (это норма, не баг). `POST/PUT /api/users`
  теперь требуют поле `name` — внешним интеграциям, дергающим эти эндпоинты, нужно его добавить.
  Откат (`down`) удаляет колонку. Порядок деплоя обычный (Web).
- **§64 — ручная (внешняя) таблица логов.** Миграция `0027_node_external_table` (применяется
  автоматически на старте) добавляет в `nodes` колонку `external_table` (`BOOLEAN NOT NULL DEFAULT
  false`) **и разово проставляет дефолтный CH-шаблон узлам с пустым `clickhouse_template_id` и
  непустым `clickhouse_table`**. Второе — не косметика: до §64 такие узлы обслуживались как обычные
  (провижининг, стартовые ALTER'ы, retention через `DROP PARTITION`), и без этого UPDATE их
  housekeeping молча прекратился бы, а таблицы росли бы бесконечно. Если дефолтного шаблона в
  `ch_templates` нет, UPDATE не выполняется и узлы сохраняют прежнее поведение. **Откат (`down`)
  удаляет только колонку** — проставленные шаблоны остаются (какие узлы были `NULL` до миграции,
  восстановить нельзя; сам шаблон безвреден — он фиксирует ровно то поведение, что было).
  Новый конфиг-ключ `web.node_default_max_body_size` (дефолт 50000) — значение «Макс. размер тела»,
  которое форма подставляет при СОЗДАНИИ узла; на существующие узлы и на API не влияет. Добавить в
  боевой `config/config.yml` (файл переносится вручную); без ключа применяется дефолт из кода.
  Порядок деплоя обычный (Web + Sender — оба читают список таблиц для стартовых ALTER'ов).
- **§63 — автор изменения узла.** Миграция `0026_node_author` добавляет в `nodes` колонки
  `created_by`/`updated_by` (`VARCHAR(255) NOT NULL DEFAULT ''`; применяется автоматически на старте,
  чисто аддитивная, существующие данные не трогает). Новой ENV/инфраструктуры нет. У узлов, созданных
  до выката, колонки пусты (в UI «Автор: —») — заполнятся при следующем сохранении/смене статуса.
  Откат (`down`) удаляет колонки. Порядок деплоя обычный (Web).
- **§71 — персональные предпочтения (дефолтный период рабочего стола per-team).** Миграция
  `0031_user_preferences` создаёт таблицу `user_preferences(user_id, team_id, key, value, updated_at)`
  с FK на `users(id)` и на `user_teams(user_id, team_id)` (оба `ON DELETE CASCADE`); применяется
  автоматически на старте, чисто аддитивная, существующие данные не трогает.
  Совместима с **PostgreSQL 12** (минимальная поддерживаемая версия — на ней работают боевые
  инсталляции): уникальность «одна запись на пользователя, команду и ключ» обеспечена индексом по
  выражению `COALESCE(team_id, …)`, а не `UNIQUE NULLS NOT DISTINCT` из PostgreSQL 15.
  Новых ENV и конфиг-ключей нет; новые эндпоинты `GET/PUT /api/me/prefs` отдаёт Web Service
  (session-only), отдельного шага деплоя не требуют.
  **Что происходит с прежней настройкой:** дефолтный период жил в `localStorage` браузера
  (`nexus.overview.period`, один на браузерный профиль). При первом заходе после обновления SPA
  переносит его в глобальный преф пользователя и удаляет из браузера; у кого его не было — стартуют
  с 24ч. Действий оператора не требуется.
  **Откат (`down`) удаляет таблицу** — сохранённые дефолты периодов теряются безвозвратно
  (некритично: восстанавливаются кликом по кнопке «По умолчанию»). Порядок деплоя обычный (Web).
- **§73 — реестр инстансов (вкладка «Настройки → Инстансы»).** Миграция `0032_peer_instances`
  создаёт таблицу `peer_instances` (название, адрес, комментарий, кеш последней проверки, авторы
  правки); применяется автоматически на старте, чисто аддитивная, существующие данные не трогает,
  FK ни на что не ставит. Совместима с **PostgreSQL 12** (`uuid_generate_v4()` из расширения
  `uuid-ossp`, созданного миграцией 0001; конструкций 13+ нет).
  **Два новых конфиг-ключа секции `web`** — добавьте их в боевой `config.yml`, иначе параметры молча
  останутся на дефолтах:
  ```yaml
  web:
    instance_probe_timeout_ms: 3000          # таймаут КАЖДОГО из двух запросов проверки
    instance_probe_rate_limit_per_min: 30    # проверок на пользователя в минуту
  ```
  **Сетевое требование — единственное, что нужно проверить руками:** проверку выполняет **Web
  Service опрашивающего инстанса**, а не браузер оператора (CORS в проекте не настроен, CSP задаёт
  `connect-src 'self'`). Значит с хоста Web должен быть доступен адрес соседа по HTTP(S). Браузерная
  доступность соседа ничего не гарантирует и наоборот.
  **На соседнем инстансе менять ничего не нужно.** Версия и активность снимаются с уже существующих
  публичных эндпоинтов `GET /api/version` и `GET /ready` — токен не требуется. Обратная сторона:
  если сосед закрыт ingress'ом с basic-auth или IP-фильтром, в списке он будет `Ошибка` (для
  basic-auth — `http 401`); лечится whitelist'ом IP опрашивающего хоста на стороне ingress.
  Если вы когда-либо решите закрыть `/api/version` или `/ready` авторизацией — помните, что этим
  сломаете реестр инстансов у всех соседей.
  Раздел admin-only; создание/изменение/удаление пишутся в журнал аудита (`instance.*`), сами
  проверки — нет. Откат (`down`) удаляет таблицу: теряется только список ссылок, работа инстансов не
  затрагивается. Порядок деплоя обычный (Web).
- **§62 — глобальный поиск узлов + история поиска.** Миграция `0025_user_search_history` создаёт
  таблицу `user_search_history(user_id, query, searched_at)` с FK на `users(id)` ON DELETE CASCADE
  (применяется автоматически на старте; чисто аддитивная, существующие данные не трогает). Новой
  ENV/инфраструктуры нет. Откат (`down`) удаляет таблицу — теряется только персональная история
  поиска. Новые эндпоинты (`GET /api/search/nodes`, `GET/POST/DELETE /api/me/search-history`) —
  session-only, отдаёт их Web Service; отдельного шага деплоя не требуют.
- **§50 — кеш узла по реальной команде + видимость редиректов.** Миграций и новой ENV нет. **Важно
  при деплое:** формат кредов в Redis-кеше конфига узла меняется (plaintext → шифр AES-256-GCM). После
  выката старые записи `node:default:*` у Receiver не расшифруются и трактуются как cache-miss с
  перечиткой из PostgreSQL (безопасно; в логах Receiver — кратковременный всплеск `decrypt cached
  auth`). Правки узлов вне команды `default` (target_url, статус, удаление, перенос) теперь применяются
  немедленно, а не по Redis-TTL. Sender по-прежнему следует 3xx-редиректам, но пишет их в служебный лог
  и в поле «Причина» лога узла (при `301/302/303` POST→GET предупреждает о потере тела).
- **Вручную** (для контролируемых деплоев — применить до старта трафика):

  | Действие | Команда (нативно) | Команда (в Docker) |
  |----------|-------------------|--------------------|
  | Применить все | `make migrate-up` | `docker compose -f <compose> run --rm web --migrate-up` |
  | Текущая версия | `make migrate-status` | `... run --rm web --migrate-status` |
  | Откатить N | `make migrate-down N=1` | `... run --rm web --migrate-down 1` |

  (Флаги `--migrate-up`/`--migrate-down N`/`--migrate-status` есть у любого из бинарей
  `web`/`receiver`/`sender`, т.к. они общие.)

> `make migrate-*` используют `--debug` (конфиг `config_debug.yml`, localhost) — это путь
> для локальной среды. На сервере без Docker запускайте бинарь напрямую с боевым
> `--config`, например: `bin/web --config config/config.yml --migrate-up`.

---

## 9. Обновление приложения и смена версии

Сервисы stateless — обновление сводится к замене кода/образов и перезапуску. Данные в
хранилищах не трогаются; новые миграции применятся автоматически на старте `web`/`receiver`.

### 9.0. Версионирование: откуда берётся версия и как присвоить новую

**Единый источник истины — git.** Версия вшивается в бинарь на этапе **сборки** из
`git describe --tags` (или имени git-тега в CI) через `ldflags` и далее без правок
доезжает в логи старта, флаг `--version`, метрики, Sentry-release и публичный
`GET /api/version` (его показывает SPA в футере, см. §30 ТЗ). **Версию руками — ни в
файлах, ни на сервере — задавать не нужно.**

Как это работает:

- **Сборка из исходников — основной (и единственный) путь.** `docker compose up -d --build`
  (прод) или `make build` (локально) вшивают версию из `git describe --tags --always --dirty`
  текущего рабочего дерева: Docker — внутри builder-стейджа (`.git` обязан быть в контексте
  сборки, см. `deploy/docker/*.Dockerfile`), `make` — через блок `version` в Makefile. Версия
  «сама подсасывается» из git, ручной правки файлов не требуется.
- **Fallback без git/ldflags** (голый `go build` без `.git`) — строка `0.0.0-dev` из
  `cmd/<svc>/versioninfo.json`. Это маркер «несборочной» версии, а **не** значение для
  бампа — поднимать его вручную НЕ нужно.

> **CI не собирает Docker-образы и не публикует их в registry.** Раньше это делал GoReleaser
> по тегу `v*`, но это требовало DinD/buildx/docker.io-auth на раннере и постоянно падало на
> инфраструктуре. Поэтому release-job убран; деплой — сборкой на сервере (§9.1).

`VERSION` в `.env` больше **не используется** (это был селектор тега образа на registry-пути,
который убран) — строку можно удалить. `web-ui/package.json` (`version`) с `/api/version` не
связан — фронт берёт версию из бэкенда.

Итого: **новая версия = новый git-тег + пересборка на сервере.** Пошаговый рецепт — в **§9.5**.

### 9.1. Обновление при сборке из исходников (варианты A/B/C)

```bash
cd nexus
git fetch --tags
git checkout <новая-версия>          # тег/ветка с нужной версией

# Версия вшьётся в образ из git автоматически (git describe текущего checkout'а,
# §9.0) — править .env для версии приложения НЕ нужно.

# Пересобрать и перекатить только сервисы приложения:
# Вариант C (основной прод, корневой docker-compose.yml — без -f):
docker compose up -d --build web receiver sender
# для варианта A (всё в Docker):
docker compose -f deploy/docker-compose.yml up -d --build web receiver sender
# для варианта B (внешние сервисы):
docker compose -f deploy/docker-compose.app.yml up -d --build
```

Compose пересоздаёт контейнеры с новыми образами; хранилища (в варианте A — в volume'ах,
в варианте B — внешние, в варианте C — внешний CH + bundled PG/Redis/Kafka в volume'ах) не
пересоздаются. Новые `*.up.sql` накатятся автоматически.

> **Рекомендация:** перед обновлением, добавляющим миграции, сделайте дамп PostgreSQL
> (`pg_dump`) — это страховка для отката БД (§10).

### 9.2. Готовые образы из registry — больше не используется

Раньше образы публиковались GoReleaser'ом в GitLab Container Registry по тегу `v*`, и прод
тянул их (`build:` → `image:` через override `deploy/docker-compose.registry.yml`). От этого
**отказались**: сборка Docker-образов в CI требовала DinD/buildx/docker.io-auth на раннере и
нестабильно работала. Деплой теперь — **сборкой из исходников на сервере** (§9.1). Registry,
override-файл и переменная `VERSION` в `.env` для этого не нужны.

### 9.3. Переход на новую мажорную версию

1. Прочитать [CHANGELOG.md](./CHANGELOG.md) на предмет breaking-changes и новых миграций.
2. Снять дамп PostgreSQL и (при критичности) бэкап ClickHouse.
3. Применить миграции контролируемо: `... run --rm web --migrate-up` **до** перезапуска
   сервисов под нагрузкой.
4. Перекатить сервисы (§9.1).
5. Проверить `/health` всех трёх сервисов и вход в UI.

### 9.4. Откуда берётся версия (git → ldflags)

Версия вшивается в бинарь **на сборке** из `git describe --tags --always --dirty` — в Docker
внутри builder-стейджа (`deploy/docker/*.Dockerfile`, нужен `.git` в контексте), локально
через блок `version` в Makefile. `git describe` срезает ведущий `v` (тег `v1.0.0` → версия
`1.0.0`). Поэтому для корректной версии на проде нужен **checkout тега с `.git`** перед
`docker compose up -d --build`. `cmd/{web,receiver,sender}/versioninfo.json` содержат только
fallback-маркер `0.0.0-dev` (сборка совсем без git) и вручную не бампятся; `web-ui/package.json`
к `/api/version` отношения не имеет.

> **Суффикс `-dirty` (например `1.0.0-dirty`).** `git describe --dirty` дописывает `-dirty`, если
> на момент сборки в рабочем дереве есть **незакоммиченные изменения отслеживаемых файлов** (staged
> или unstaged; untracked-файлы не считаются). Так как образ собирается из текущего checkout'а
> (`COPY . .` копирует дерево как есть), любая локальная правка на сервере уедет в версию образа —
> и `/api/version` перестанет соответствовать тегу. **Перед `--build` дерево должно быть чистым:**
> собирай из свежего detached-checkout тега и проверяй `git status --porcelain` (должно быть пусто).
> Частый самострел — пересборка генерируемых, но отслеживаемых файлов (`docs/` через `make swagger`,
> `internal/web/static/` через `make build-ui`) без коммита: закоммить их до сборки.
>
> **Ложный `-dirty` при чистом `git status` на сервере.** `git describe`, в отличие от
> `git status`, **не освежает** stat-кэш индекса. После `COPY . .` у файлов в Docker-слое новые
> `mtime`/`inode`, и `git describe --dirty` без refresh принимает неизменённое дерево за грязное —
> версия уезжает как `v1.2.0-dirty`, хотя на хосте `git status` чист. Поэтому в
> `deploy/docker/*.Dockerfile` перед `git describe` стоит `git update-index -q --refresh` (он
> сверяет содержимое и обновляет stat-кэш; реальная грязь по-прежнему даёт `-dirty`). Если правишь
> эти Dockerfile — **не выкидывай** строку с `update-index`, иначе ложный суффикс вернётся.

### 9.5. Выпуск новой версии (тег → сборка на сервере)

**Коротко — три шага.** Версия = git-тег; деплой = пересборка на сервере из этого тега
(образы в CI/registry не собираются, §9.2).

```bash
# 1. Рабочая машина: слить релизный код dev → master и поставить тег.
git switch master
git merge --no-ff dev
git push origin master
git tag -a v1.0.0 -m "Release 1.0.0"        # тег на коммите master
git push origin v1.0.0                      # CI прогонит test/lint/build (валидация)

# 2. Прод-сервер: подтянуть тег и пересобрать (версия вшьётся из git).
cd nexus
git fetch --tags
git checkout v1.0.0                         # checkout С .git — нужен для git describe
git status --porcelain                      # ДОЛЖНО быть пусто — иначе версия уедет как "-dirty" (§9.4)
docker compose up -d --build web receiver sender         # Вариант C (корневой compose)
#   A: docker compose -f deploy/docker-compose.yml up -d --build web receiver sender
#   B: docker compose -f deploy/docker-compose.app.yml up -d --build

# 3. Проверка.
curl -s http://<host>:8000/api/version       # → {"version":"1.0.0"}
```

> Тег `v*` запускает в CI обычные test/lint/build (валидация кода), но **не** собирает
> образы — их собирает сам сервер при `--build`. Откат — `git checkout` прежнего тега +
> `up -d --build` (§10.1).

Полный чек-лист (пример — `1.0.0`, Вариант C, внешний ClickHouse):

**A. Подготовка релиза (рабочая машина):**

- [ ] Код проходит CI на `dev` (`make test`, `golangci-lint run`, `cd web-ui && npm run lint && npm run build`).
- [ ] Встроенный SPA пересобран и закоммичен (`make build-ui` → `internal/web/static/`), если менялся `web-ui/`.
- [ ] Обновлён [CHANGELOG.md](./CHANGELOG.md) (фичи/фиксы/breaking-changes, список миграций).
- [ ] **Релиз меняет структуру CH-таблиц логов (новая/изменённая колонка в `RequiredLogColumns`)?**
      В описание релиза (секция CHANGELOG) ОБЯЗАТЕЛЬНО включён SQL для ручного запуска владельцами
      **внешних таблиц §64** (Nexus их не альтерит; имя `<db>.<table>` владелец подставляет сам) +
      примечание о поведении в деплой-окне. Образец — запись §67 (`client_host`).
- [ ] **Релиз содержит §69 (валидация схемы `target_url`)?** Проверить, нет ли на бою узлов с
      адресом без схемы — с версии §69 их сохранение, копирование и dry-run отдают 400, пока адрес
      не поправят вручную (автоматической миграции нет: `http` vs `https` не угадать):
      ```sql
      SELECT id, path, team_id, url_mode, target_url FROM nodes
      WHERE url_mode = 'static' AND target_url <> '' AND target_url !~* '^https?://';
      ```
      Пусто — деплой без последствий. Есть строки — поправить адреса до раскатки.
- [ ] Код слит в `master`: `git switch master && git merge --no-ff dev && git push origin master`.
- [ ] Поставлен и запушен тег `vX.Y.Z`: `git tag -a v1.0.0 -m "Release 1.0.0" && git push origin v1.0.0`.
      Версию в файлах поднимать НЕ нужно — её даёт git-тег при сборке (§9.0/§9.4).

**B. Подготовка прод-сервера (один раз):**

- [ ] Репозиторий склонирован **с `.git`** (нужен для `git describe` при сборке образов).
- [ ] В `.env` заданы реальные `CH_HOST/CH_PORT/CH_USER/CH_PASSWORD` внешнего ClickHouse и
      **обязательный** `ENCRYPTION_KEY` (32 байта base64, §2). `VERSION` не нужен (registry-путь убран).
- [ ] Single-broker Kafka? Заданы `KAFKA_TOPIC_REPLICATION_FACTOR=1` и `KAFKA_TOPIC_MIN_INSYNC_REPLICAS=1` (§2).
- [ ] Используете дашборды панели / Telegram-алерты? Задан `PROMETHEUS_URL` (§21/§22).
- [ ] У CH-пользователя есть право создавать БД/таблицы (`nexus_<slug>`, §5.3).

**C. Раскатка:**

- [ ] **Перед обновлением с новыми миграциями** снят дамп PostgreSQL (`pg_dump`, §12) — страховка отката.
- [ ] `git fetch --tags && git checkout v1.0.0`.
- [ ] `docker compose up -d --build web receiver sender` (Вариант C; для A/B — со своим `-f`).
- [ ] Миграции применились на старте `web`/`receiver` (в логах нет ошибок миграций, §8).

**D. Проверка:**

- [ ] `/health` всех трёх сервисов отвечает: `:8000` (web), `:8080` (receiver), `:9093` (sender).
- [ ] `GET /api/version` возвращает `1.0.0`; в футере SPA та же версия.
- [ ] Вход в UI под `admin` работает; ключевые сценарии (создание узла, sync/async-запрос,
      просмотр логов) проходят — при сомнениях сверьтесь с [docs/STAND_TESTING.md](./docs/STAND_TESTING.md).

> **Откат:** `git checkout` прежнего тега → `docker compose up -d --build` (§10.1). Схему БД
> откатывать только при несовместимости и только с дампом (§10.2).

---

## 10. Откат на предыдущую версию

Откат состоит из двух независимых частей: **код приложения** и **схема БД**.

### 10.1. Откат кода (быстрый, безопасный)

```bash
git fetch --tags
git checkout <предыдущий-тег>
docker compose up -d --build web receiver sender    # Вариант C; для A/B — со своим -f
```

Если новая версия **не добавляла миграций**, этого достаточно — схема совместима.

### 10.2. Откат схемы БД (только при необходимости)

Откатывайте миграции **только** если новая версия добавила несовместимые с предыдущим
кодом изменения схемы. Down-миграции могут удалять колонки/таблицы → **возможна потеря
данных**. Перед откатом обязательно снимите дамп.

```bash
# узнать текущую версию схемы:
docker compose -f deploy/docker-compose.yml run --rm web --migrate-status

# откатить N последних миграций (N = число миграций, добавленных новой версией):
docker compose -f deploy/docker-compose.yml run --rm web --migrate-down 1
```

Порядок безопасного отката с изменением схемы:

1. Остановить сервисы приложения (трафик): `docker compose ... stop web receiver sender`.
2. (Опционально) восстановить дамп PostgreSQL, снятый перед обновлением — самый надёжный
   способ вернуть данные в прежнее состояние.
3. Либо выполнить `--migrate-down N`, если down-миграции не теряют нужных данных.
4. Развернуть предыдущую версию кода (§10.1).
5. Запустить сервисы и проверить `/health`.

> ClickHouse-логи и Kafka-очередь при откате кода обычно не трогаются (формат лога
> стабилен). Если новая версия меняла схему таблиц логов — сверьтесь с CHANGELOG.

---

## 11. Запуск под Docker на Windows

На Windows используется **Docker Desktop** (WSL2-бэкенд). Команды `docker compose`
идентичны Linux — отличается только генерация `ENCRYPTION_KEY` и оболочка (PowerShell).

### 11.1. Полностью в Docker (все зависимости в контейнерах)

```powershell
# 1. Установить Docker Desktop и включить WSL2-интеграцию.
# 2. Подготовить секреты:
Copy-Item .env.example .env
#    Отредактируйте .env (пароли + ENCRYPTION_KEY). Сгенерировать ключ:
[Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))

# 3. Поднять стек:
docker compose -f deploy/docker-compose.yml up -d --build

# 4. Bootstrap пароля admin:
docker compose -f deploy/docker-compose.yml run --rm web --set-admin-password 'СильныйПароль'

# 5. Проверка:
curl.exe http://localhost:8000/health
```

UI — `http://localhost:8000/`. Миграции применяются автоматически (§8).

### 11.2. Docker + внешние сервисы на Windows

Тот же `docker-compose.app.yml`, что и в §4. В `.env` укажите адреса внешних сервисов;
для сервисов, запущенных на самом Windows-хосте, используйте `host.docker.internal`:

```powershell
# .env (фрагмент):
#   PG_HOST=host.docker.internal
#   CH_HOST=host.docker.internal
#   REDIS_HOST=host.docker.internal
#   KAFKA_BROKERS=host.docker.internal:9092

docker compose -f deploy/docker-compose.app.yml up -d --build
docker compose -f deploy/docker-compose.app.yml run --rm web --set-admin-password 'СильныйПароль'
```

> Для разработки/отладки на Windows (поднять зависимости в Docker, а сами сервисы
> запускать под отладчиком VS Code) — см. [DEVELOPMENT.md](./DEVELOPMENT.md).

---

## 12. Бэкап и восстановление (кратко)

- **PostgreSQL** (критично — конфиг узлов, пользователи, секреты):
  ```bash
  docker compose -f deploy/docker-compose.yml exec postgres pg_dump -U nexus nexus > nexus_pg.sql
  ```
- **`ENCRYPTION_KEY`** — храните в защищённом месте. Без него зашифрованные креды узлов в
  PostgreSQL не расшифруются. Ротация ключа — `make rotate-encryption-key OLD_KEY=... NEW_KEY=...`.
- **ClickHouse** — логи; стратегия бэкапа зависит от объёма (партиции по дням,
  housekeeping с retention). Для большинства сценариев логи не бэкапятся.

---

## 13. Типовые проблемы

| Симптом | Причина / решение |
|---------|-------------------|
| Сервис падает на старте с `ENCRYPTION_KEY invalid` | base64 не декодируется в ровно 32 байта — перегенерируйте (§2). |
| `InvalidReplicationFactor` при старте `sender` | Single-broker Kafka: задайте `KAFKA_TOPIC_REPLICATION_FACTOR=1` и `KAFKA_TOPIC_MIN_INSYNC_REPLICAS=1` в `.env`. |
| Kafka не поднимается за 10 сек | Норма для KRaft — дайте до 30 сек (`start_period`). Логи: `docker compose ... logs kafka`. |
| `web` стартует, но replay/live-tail отдают 404 | ClickHouse недоступен — некритично, остальное работает. Проверьте `CH_HOST`/`CH_PASSWORD`. |
| Не получается войти под `admin` | Не выполнен bootstrap пароля (§3.3) — пароль остаётся NULL после миграции. |
| Внешний сервис на хосте недоступен из контейнера | Используйте `host.docker.internal` в `.env` (проброшен в `docker-compose.app.yml`). |
| Узел на внутреннем домене отвечает `x509: certificate signed by unknown authority` | Корпоративный CA не в trust store контейнера. В образ Sender'а уже встроен **промежуточный** «Vozovoz Issuing CA» ([deploy/certs/](deploy/certs/README.md)) — пересоберите образ (`--build`). **Корневой** «Vozovoz Root CA» добавлять бесполезно: у него нет `basicConstraints: CA:TRUE`, Go отвергает его в любом случае (`parent certificate cannot sign this kind of certificate`). Подробности и способ обновить сертификат — в [deploy/certs/README.md](deploy/certs/README.md). |
| Узел на **внешнем** домене отвечает `x509: certificate signed by unknown authority`, следом — `circuit_breaker_open` | Сервер переехал на CA, которого нет в бандле alpine. Так было 29.07.2026 с боевым эквайрингом Альфа-Банка (`pay.alfabank.ru` → НУЦ Минцифры) — узел `qr` встал целиком. Проверьте издателя: `echo \| openssl s_client -connect <host>:443 -servername <host> \| openssl x509 -noout -issuer`. Если это НУЦ Минцифры — его корень уже встроен в образ Sender'а с 1.20.1 ([deploy/certs/](deploy/certs/README.md)), достаточно пересобрать образ (`--build`). Если другой УЦ — добавьте его корень по образцу из [deploy/certs/README.md](deploy/certs/README.md). Circuit breaker закроется сам после восстановления доставки. |
| Тестовый запрос (dry-run) со снятой галкой mock отдаёт `real call is not available: web.sender_grpc.addr is not configured` | В конфиге Web нет секции `web.sender_grpc` — она опциональна и намеренно без дефолта (§55, см. выше). Добавьте `addr: sender:9190` и перезапустите **только** Web. |
