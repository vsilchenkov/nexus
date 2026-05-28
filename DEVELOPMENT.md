# DEVELOPMENT.md — локальная разработка и отладка в VS Code (Windows)

Как запустить и отлаживать Nexus на Windows из VS Code: от первого запуска и миграций
до старта всех трёх сервисов под отладчиком одной кнопкой.

> Развёртывание в продакшене (Docker, Linux-сервер, внешние сервисы, обновление, откат) —
> в отдельном файле [DEPLOYMENT.md](./DEPLOYMENT.md).

---

## 1. Сценарий

Зависимости (PostgreSQL, Redis, ClickHouse, Kafka) поднимаются в Docker Desktop отдельным
self-contained compose-файлом с пробросом портов на хост. Сами сервисы `receiver`/`sender`/`web`
запускаются **локально под отладчиком dlv** и цепляются к `localhost:<port>`.

```
                Docker Desktop                         Хост (под отладчиком VS Code)
   ┌───────────────────────────────────┐        ┌──────────────────────────────────┐
   │ postgres:5432  redis:6379          │◄───────│  web      → :8000                 │
   │ clickhouse:8123/9000  kafka:9092   │◄───────│  receiver → :8080                 │
   └───────────────────────────────────┘        │  sender   → :9090 (gRPC) / :9091  │
        docker-compose.deps.yml                  └──────────────────────────────────┘
```

В каталоге [.vscode/](./.vscode/) уже лежат готовые конфиги: `launch.json` (запуск/отладка
сервисов), `tasks.json` (зависимости, миграции, тесты), `settings.json` (gopls/dlv).

---

## 2. Разовая подготовка

1. **Docker Desktop** — установить, включить WSL2-интеграцию, запустить.
2. **Go 1.26+** — установить, проверить `go version`.
3. **VS Code + расширение `golang.go`.** Через Command Palette (`Ctrl+Shift+P`) →
   **Go: Install/Update Tools** поставить `dlv`, `gopls`, `golangci-lint`.
4. **`.env` в корне репозитория** на основе [.env.example](./.env.example). Минимально
   обязателен `ENCRYPTION_KEY` — 32 байта в base64. Сгенерировать в PowerShell:

   ```powershell
   [Convert]::ToBase64String((1..32 | ForEach-Object { Get-Random -Maximum 256 }))
   ```

   Остальные `PG_*`/`REDIS_*`/`CH_*`/`KAFKA_BROKERS` для локальной отладки можно не трогать:
   конфиг `config/config_debug.yml` уже указывает на `localhost`, а значения из `.env`
   перекрывают дефолты через плейсхолдеры `${VAR:default}`.

---

## 3. Порядок первого запуска

### Шаг 1. Поднять зависимости

```powershell
docker compose -f deploy/docker-compose.deps.yml up -d
```

или Command Palette → **Tasks: Run Task → deps: up**.

Файл [deploy/docker-compose.deps.yml](./deploy/docker-compose.deps.yml) самодостаточный:
поднимает только зависимости с портами `5432/6379/8123/9000/9092` на хосте. Kafka
анонсирует себя как `localhost:9092` (именно для процессов с хоста).

Проверка состояния — **deps: status**; логи Kafka — **deps: logs (kafka)**; полный сброс
данных — **deps: down + reset volumes**.

> Prometheus спрятан за профилем `metrics` и по умолчанию выключен (его scrape-таргеты
> рассчитаны на контейнерные имена). Для метрик: `docker compose -f deploy/docker-compose.deps.yml --profile metrics up -d`.

> **Дашборды панели (§21) локально.** KPI на Overview, per-node throughput и график на
> странице узла берутся из Prometheus + ClickHouse. По умолчанию `prometheus.url` в
> `config_debug.yml` пуст — панель работает, но эти блоки показывают «нет данных»
> (`prometheus_available=false`). Чтобы увидеть реальные числа локально:
> 1. подними профиль `metrics` (команда выше) — Prometheus слушает `localhost:9091`;
> 2. задай `PROMETHEUS_URL=http://localhost:9091` перед запуском `cmd/web` (env подставится
>    в `${PROMETHEUS_URL:}` плейсхолдер конфига).
>
> Важно: scrape-таргеты Prometheus в `deploy/prometheus.yml` указывают на контейнерные имена
> (`receiver:8080`, `sender:9091`, `web:8000`). При нативном запуске сервисов с хоста
> Prometheus их не доскрейпит — поэтому глобальные KPI/очередь будут пустыми даже с заданным
> `PROMETHEUS_URL`. Per-node KPI и график на странице узла при этом работают всегда (источник —
> ClickHouse). Полноценные дашборды проще смотреть в полном docker-compose, где все сервисы
> в одной сети с Prometheus.

### Шаг 2. Применить миграции (один раз / после новых миграций)

- **Tasks: Run Task → migrate up**, либо в терминале:
  ```powershell
  go run ./cmd/web --debug --migrate-up
  ```
- Проверить версию схемы: `go run ./cmd/web --debug --migrate-status`
- Откатить последнюю: `go run ./cmd/web --debug --migrate-down 1`

Либо через Run and Debug — конфиг **Web: migrate-up** (миграция под отладчиком).

> При обычном `--debug`-запуске `web` и `receiver` и так применяют миграции автоматически.
> Отдельный шаг нужен, когда хотите накатить схему до старта сервисов или явно проверить статус.

### Шаг 3. Задать пароль администратора (один раз)

Миграция создаёт `admin` с пустым паролем. Задать:

- **Tasks: Run Task → set admin password (admin)** (ставит пароль `admin`), либо:
  ```powershell
  go run ./cmd/web --debug --set-admin-password admin
  ```
- Либо Run and Debug → **Web: set-admin-password**.

### Шаг 4. Запустить сервисы под отладчиком

Откройте панель **Run and Debug** (`Ctrl+Shift+D`) и запустите нужный конфиг (см. §4).
Для полного стека — **Nexus: all (web + receiver + sender)**.

---

## 4. Варианты запуска в VS Code

Конфиги из [.vscode/launch.json](./.vscode/launch.json):

| Конфиг                              | Что делает                                                       |
|-------------------------------------|------------------------------------------------------------------|
| `Web (debug)`                       | `go run ./cmd/web --debug` под dlv → `:8000`                     |
| `Receiver (debug)`                  | `go run ./cmd/receiver --debug` под dlv → `:8080`               |
| `Sender (debug)`                    | `go run ./cmd/sender --debug` под dlv → `:9090` gRPC + `:9091`  |
| `Nexus: all (web + receiver + sender)` | **compound** — все три сервиса одной кнопкой (`stopAll: true`) |
| `Web: migrate-up`                   | разовая миграция под отладчиком                                  |
| `Web: set-admin-password`           | задаёт пароль `admin`                                            |
| `Loadtest`                          | `cmd/loadtest` против локального стека (1m / 100 RPS / 10 узлов) |
| `Integration tests (current file)`  | `go test -tags=integration -v` для открытого файла              |
| `Attach to process (pid)`           | подключение отладчика к уже запущенному процессу                |

Все конфиги читают переменные из `${workspaceFolder}/.env` (`envFile`) и используют
`--debug` → [config/config_debug.yml](./config/config_debug.yml) (все хосты — `localhost`).
Точки останова, шаг с заходом, инспекция переменных — штатно через dlv-dap.

### Запуск всего сервиса целиком

`Nexus: all` — это **compound**-конфигурация: одним запуском поднимает `Web`, `Sender` и
`Receiver` (с `stopAll: true` — остановка любого гасит все три). Это и есть «весь сервис»
под отладчиком. Предусловие — выполнены шаги 1–3 из §3 (зависимости подняты, миграции
применены, пароль задан).

### Задачи (Tasks: Run Task) из [.vscode/tasks.json](./.vscode/tasks.json)

| Задача | Команда |
|--------|---------|
| `deps: up` / `deps: down` / `deps: status` | управление зависимостями в Docker |
| `deps: down + reset volumes` | снести зависимости вместе с данными |
| `deps: logs (kafka)` | стрим логов Kafka |
| `migrate up` | `go run ./cmd/web --debug --migrate-up` |
| `set admin password (admin)` | `go run ./cmd/web --debug --set-admin-password admin` |
| `test: unit` | `go test -race -short ./...` |
| `test: integration` | `go test -tags=integration -count=1 -v ./tests/integration/...` |
| `build: all` | сборка `bin/{receiver,sender,web}.exe` |

---

## 5. После запуска

- Web UI / API → `http://localhost:8000`
- Swagger UI → `http://localhost:8000/swagger/index.html`
- Receiver → `http://localhost:8080/v1/request/*`, `http://localhost:8080/v1/requestAsync/*`
- Sender admin → `http://localhost:9091/health` (сам gRPC SenderService — на `:9090`)

Логин в UI: `admin` + пароль, заданный на шаге 3.

---

## 6. Запуск на уже поднятых внешних сервисах (Docker Desktop)

Если зависимости уже крутятся в Docker Desktop по **своей** конфигурации (отличной от
`docker-compose.deps.yml`) — с другими логинами, паролями, портами или именами БД, — то
`docker-compose.deps.yml` поднимать не нужно: достаточно привести конфиг Nexus в соответствие
с этими сервисами. Ниже — на примере типового стороннего `docker-compose.yml`:

| Сервис      | Адрес с хоста        | Логин      | Пароль   | Что важно учесть                                              |
|-------------|----------------------|------------|----------|---------------------------------------------------------------|
| PostgreSQL  | `localhost:5432`     | `postgres` | `vOkjDn` | БД по умолчанию `postgres`; Nexus жёстко требует БД `nexus` — её надо создать |
| Redis       | `localhost:6379`     | `sa`       | `I2MV5s` | default-юзер выключен через ACL → нужен `username`             |
| ClickHouse  | `localhost:`**`19000`** | `default` | —        | native-порт проброшен как **19000** (не 9000); HTTP — 18123   |
| Kafka       | `localhost:9092`     | —          | —        | брокер анонсирует себя как `kafka:9092` → нужен hosts-маппинг  |

### Шаг 1. Подготовить сервисы

**а) Создать БД `nexus` в PostgreSQL** (имя БД в конфиге Nexus фиксированное, миграции его не
создают):

```powershell
docker exec -i postgres psql -U postgres -c "CREATE DATABASE nexus;"
```

**б) Создать БД `nexus_default` в ClickHouse.** Это база CH default-команды — именно сюда пишутся
логи (`nodes.clickhouse_table` хранит полное `nexus_<slug>.<table>`, поле нормализуется в Web при
создании/обновлении узла). Сидинг default-команды в миграции
0008 создаёт только строку в PostgreSQL — саму CH-базу для неё никто не заводит автоматически
(`TeamProvisioner` создаёт `nexus_<slug>` только при создании команды через UI `/api/teams`).
Плюс Sender при старте подключается к БД из `clickhouse.database`, поэтому она должна существовать:

```powershell
docker exec -i clickhouse clickhouse-client --query "CREATE DATABASE IF NOT EXISTS nexus_default"
```

Таблицы логов внутри `nexus_default` Nexus создаёт сам при создании узла с CH-шаблоном (§19) —
вручную их заводить не нужно, у CH-юзера `default` прав на `CREATE` достаточно. Для дополнительных
команд (`/api/teams`) их базы `nexus_<slug>` создаются провижионером автоматически.

**в) Замапить `kafka` на localhost.** Брокер анонсирует адрес `kafka:9092`
(`KAFKA_ADVERTISED_LISTENERS`), поэтому процесс на хосте после bootstrap'а получит `kafka:9092`
и не сможет его разрешить. Добавьте в `C:\Windows\System32\drivers\etc\hosts` (от администратора)
строку:

```text
127.0.0.1 kafka
```

> Альтернатива без правки hosts — сменить в вашем `docker-compose.yml`
> `KAFKA_ADVERTISED_LISTENERS` на `PLAINTEXT://localhost:9092` и пересоздать контейнер kafka.

### Шаг 2. Привести конфиг под свои сервисы

`--debug` использует [config/config_debug.yml](./config/config_debug.yml) с localhost-дефолтами
(PG `nexus`/`nexus`, Redis без пароля, CH порт 9000). Эти значения **зашиты в файл и НЕ
переопределяются через `.env`** — их нужно поправить. Выберите способ:

**Способ A (рекомендуемый для VS Code) — отредактировать `config/config_debug.yml`.** Тогда все
существующие launch-конфиги (`--debug`), `make run-*` и таски работают без изменений. Замените
три секции на свои значения (Kafka трогать не нужно — `brokers: localhost:9092` уже подходит
после hosts-маппинга):

```yaml
postgres:
  host: localhost
  port: 5432
  database: nexus
  user: postgres
  password: vOkjDn
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime_min: 30

redis:
  host: localhost
  port: 6379
  db: 0
  username: sa
  password: I2MV5s
  pool_size: 10
  min_idle_conns: 2
  dial_timeout_ms: 2000
  read_timeout_ms: 1000
  write_timeout_ms: 1000
  node_ttl_sec: 60
  session_ttl_sec: 3600
  ratelimit_window_sec: 60

clickhouse:
  host: localhost
  port: 19000
  database: nexus_default   # БД подключения по умолчанию (= CH-база default-команды)
  user: default
  password: ""
  batch_size: 100
  flush_interval_sec: 2
  buffer_max_size: 10000
  workers: 1
```

`config_debug.yml` отслеживается git'ом — чтобы случайно не закоммитить локальные креды и чтобы
правки не висели в `git status`:

```powershell
git update-index --skip-worktree config/config_debug.yml
# вернуть отслеживание при необходимости:
# git update-index --no-skip-worktree config/config_debug.yml
```

**Способ B (изолированный) — отдельный файл.** Скопируйте `config/config_debug.yml` в
`config/config.local.yml`, внесите те же три правки и запускайте с `--config config/config.local.yml`
вместо `--debug`. Для VS Code продублируйте нужные конфиги в [.vscode/launch.json](./.vscode/launch.json),
заменив `"args": ["--debug"]` на `"args": ["--config", "config/config.local.yml"]`. Добавьте
`config/config.local.yml` в `.gitignore`.

### Шаг 3. Первый запуск и миграция

`.env` с валидным `ENCRYPTION_KEY` всё равно обязателен (см. §2). Дальше — миграции и bootstrap
пароля (способ A — с `--debug`; способ B — подставьте `--config config/config.local.yml`):

```powershell
# применить миграции (создаст таблицы в БД nexus)
go run ./cmd/web --debug --migrate-up

# проверить версию схемы
go run ./cmd/web --debug --migrate-status

# задать пароль admin
go run ./cmd/web --debug --set-admin-password admin
```

То же доступно через Run and Debug (**Web: migrate-up**, **Web: set-admin-password**) и таски
(**migrate up**, **set admin password (admin)**).

### Шаг 4. Запуск сервисов

- **Способ A:** как обычно — Run and Debug → `Nexus: all (web + receiver + sender)` или
  `make run-receiver` / `run-sender` / `run-web`.
- **Способ B:** `go run ./cmd/web --config config/config.local.yml` (по сервису на терминал) или
  запуск добавленных в `launch.json` конфигов.

Проверка работоспособности — раздел §5.

---

## 7. Типовые грабли

- **`ENCRYPTION_KEY invalid`** — декодированный base64 не равен 32 байтам, перегенерируйте
  (см. §2).
- **`clickhouse connect failed` в Web** — некритично: replay/live-tail отключатся, остальное
  работает (см. [bootstrap.go](./internal/platform/bootstrap/bootstrap.go)).
- **Kafka не стартует за 10 сек** — норма для KRaft на Windows; дайте 30 сек. Логи:
  `docker compose -f deploy/docker-compose.deps.yml logs kafka`.
- **Порт `5432`/`6379`/`9092` занят** — выключите локальный сервис postgres/redis/kafka или
  поменяйте проброс портов в `docker-compose.deps.yml`.
- **`go test -race` падает на cgo** — на Windows нужен gcc из MSYS2 в PATH (см. шапку
  [Makefile](./Makefile): mingw64 добавляется в PATH автоматически при наличии).
- **delve: «could not load source» для `embed.FS`** — не критично, на отладку не влияет
  (см. [.vscode/settings.json](./.vscode/settings.json)).
