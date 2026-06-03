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
| Kafka       | 3.9 (KRaft) | `9092`     | Очередь async-запросов (`nexus.async`/`.dlq`) |
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
| `VERSION`             | Версия приложения (попадает в `build.version`, `--version`, метрики) | `0.1.0`  |
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

Дополнительно при single-broker Kafka (один узел) задавайте в `.env`
`KAFKA_TOPIC_REPLICATION_FACTOR=1` и `KAFKA_TOPIC_MIN_INSYNC_REPLICAS=1` — иначе создание
топиков упадёт с `InvalidReplicationFactor` (дефолты в `config.example.yml` рассчитаны
на кластер из 3+ брокеров: RF=3, ISR=2).

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

**§28 — Публичный адрес приложения (после публикации за доменом/reverse-proxy).** Полный адрес
узла, который показывает UI (`<origin>/api/v1/<verb>/<path>` + кнопка «Скопировать»), по умолчанию
берётся от адреса хоста в браузере. Когда приложение опубликовано под доменом (за reverse-proxy),
задайте публичный адрес в **Settings → Общие → Публичный адрес приложения** (например
`https://nexus.example.com`, только origin без пути и хвостового слеша). Значение хранится в
`app_settings` (БД, не env), правится admin'ом без рестарта; пустое — снова используется origin
браузера.

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
  создайте `nexus.async` и `nexus.async.dlq` (Nexus сам пытается их завести с retention
  30 дней; на single-broker не забудьте RF=1/ISR=1 — см. §2).
- **Redis**: при включённом ACL задайте `REDIS_USER` и `REDIS_PASSWORD`.

---

## 5. Вариант C — Docker, внешний только ClickHouse (PG/Redis/Kafka — в Docker)

Когда есть отдельный (управляемый или кластерный) ClickHouse, а PostgreSQL, Redis и Kafka
удобнее держать рядом в Docker. Поднимается через
[deploy/docker-compose.ch-external.yml](./deploy/docker-compose.ch-external.yml): bundled
PostgreSQL + Redis + Kafka + Prometheus + три сервиса Nexus, **без** контейнера ClickHouse.

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

```bash
docker compose -f deploy/docker-compose.ch-external.yml up -d --build

# bootstrap пароля admin:
docker compose -f deploy/docker-compose.ch-external.yml run --rm web --set-admin-password 'СильныйПароль'

docker compose -f deploy/docker-compose.ch-external.yml ps
docker compose -f deploy/docker-compose.ch-external.yml logs -f web receiver sender
```

Миграции PostgreSQL применяются автоматически (§8) — БД `nexus` и Redis/Kafka подняты в этом
же стеке, готовить их вручную не нужно.

### 5.3. Предусловия на стороне ClickHouse

Только для ClickHouse (как в §4.3): CH-юзер должен иметь право создавать БД и таблицы — Nexus
заводит БД на команду (`nexus_<slug>`, для default — `nexus_default`) и создаёт таблицы логов.
PostgreSQL/Redis/Kafka — bundled, как в Варианте A.

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

## 7. Переопределение `config.yml` в Docker (опционально)

Если нужно изменить параметры приложения (например, увеличить пулы или таймауты), не
пересобирая образ, — смонтируйте свой `config.yml` поверх зашитого. Создайте файл
`deploy/docker-compose.override.yml`:

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

Compose автоматически подхватывает `docker-compose.override.yml`, либо укажите его явно
через `-f`. Плейсхолдеры `${VAR}` в смонтированном `config.yml` по-прежнему подставляются из `.env`.

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

Версия отдаётся в логах старта, флаге `--version`, метриках и публичном
`GET /api/version` (его показывает SPA в футере, см. §30 ТЗ). Источник версии:

- **Релизная сборка (рекомендуемый путь) — git-тег + GoReleaser.** Версия
  проставляется через `ldflags` в `build.Version`/`build.Commit`/`build.BuildDate`
  (см. `.goreleaser.yaml`). Чтобы выпустить новую версию: создать и запушить тег
  `vX.Y.Z` (`git tag vX.Y.Z && git push origin vX.Y.Z`) — CI соберёт образы
  `{receiver,sender,web}:X.Y.Z` с этой версией. **Ручной правки файлов не требуется.**
- **Локальная сборка / Windows-бинарь без ldflags** берёт версию из
  `cmd/<svc>/versioninfo.json` (`StringFileInfo.ProductVersion`). При выпуске новой
  версии без тега синхронно поднимите `ProductVersion` во **всех трёх** файлах
  `cmd/{web,receiver,sender}/versioninfo.json`.
- **Docker / метрики** дополнительно читают `VERSION` из `.env` (см. карту переменных
  §2) — держите его согласованным с тегом.
- **`web-ui/package.json`** (`version`) — справочное значение фронта; на отдаваемую
  `GET /api/version` не влияет (фронт берёт версию из бэкенда), но держите в синхроне.

Итого при ручном бампе версии (без релизного тега) обновите: три `versioninfo.json`,
`.env` (`VERSION=`), `web-ui/package.json`. При релизе через тег — достаточно тега.

### 9.1. Обновление при сборке из исходников (варианты A/B/C)

```bash
cd nexus
git fetch --tags
git checkout <новая-версия>          # тег/ветка с нужной версией

# (опционально) зафиксировать версию в .env для метрик и --version:
#   VERSION=1.4.0

# Пересобрать и перекатить только сервисы приложения:
docker compose -f deploy/docker-compose.yml up -d --build web receiver sender
# для варианта B:
docker compose -f deploy/docker-compose.app.yml up -d --build
# для варианта C:
docker compose -f deploy/docker-compose.ch-external.yml up -d --build web receiver sender
```

Compose пересоздаёт контейнеры с новыми образами; хранилища (в варианте A — в volume'ах,
в варианте B — внешние, в варианте C — внешний CH + bundled PG/Redis/Kafka в volume'ах) не
пересоздаются. Новые `*.up.sql` накатятся автоматически.

> **Рекомендация:** перед обновлением, добавляющим миграции, сделайте дамп PostgreSQL
> (`pg_dump`) — это страховка для отката БД (§10).

### 9.2. Обновление через готовые образы из registry

Релизные образы публикуются GoReleaser'ом в GitLab Container Registry по тегу `v*`:
`$CI_REGISTRY_IMAGE/{receiver,sender,web}:<version>`. Чтобы запускать их вместо локальной
сборки, в override-файле замените `build:` на `image:` с нужным тегом и выполните
`docker compose pull && docker compose up -d`. Смена версии = смена тега образа.

### 9.3. Переход на новую мажорную версию

1. Прочитать [CHANGELOG.md](./CHANGELOG.md) на предмет breaking-changes и новых миграций.
2. Снять дамп PostgreSQL и (при критичности) бэкап ClickHouse.
3. Применить миграции контролируемо: `... run --rm web --migrate-up` **до** перезапуска
   сервисов под нагрузкой.
4. Перекатить сервисы (§9.1).
5. Проверить `/health` всех трёх сервисов и вход в UI.

---

## 10. Откат на предыдущую версию

Откат состоит из двух независимых частей: **код приложения** и **схема БД**.

### 10.1. Откат кода (быстрый, безопасный)

```bash
# из исходников:
git checkout <предыдущий-тег>
docker compose -f deploy/docker-compose.yml up -d --build web receiver sender

# из registry: верните прежний тег образа в override и
docker compose pull && docker compose up -d web receiver sender
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
