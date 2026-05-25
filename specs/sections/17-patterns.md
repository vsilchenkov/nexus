## 17. Паттерны разработки

Раздел фиксирует архитектурные паттерны, которым следует код. Это не догма, а набор соглашений — отступление от них требует осознанного обоснования в код-ревью.

### 17.1 Общее: связь фронта и бэка

**Один процесс, один бинарь.** Web Service (`/cmd/web`) — это Go-приложение, которое:
- Отдаёт REST API под префиксом `/api/*`.
- Отдаёт статику собранного SPA (HTML, JS, CSS, assets) под всеми остальными путями через `embed.FS` (стандартная библиотека Go).
- Для SPA-роутинга (history mode) использует **fallback на `index.html`** для всех путей, не начинающихся с `/api/`, `/swagger/`, `/metrics`, `/health`, `/ready`.

**Почему так:** один бинарь — одна команда деплоя (`docker run databus-web`), один процесс в `docker-compose.yml`, нет CORS-боли (один origin), нет sync между версиями фронта и бэка (они в одном артефакте). Минус — фронт пересобирается одновременно с Go-бинарём; компенсируется отдельной целью `make build-ui` и dev-режимом, где фронт запускается через Vite-dev-server против запущенного Go-бэка.

**Структура сборки:**
```
/web-ui                  # исходники SPA (TS, JSX, package.json)
  /src
  /dist                  # output Vite-сборки (в .gitignore)
/internal/web/static     # embed.FS целиком монтирует /web-ui/dist
```

Перед `go build` цель Makefile запускает `make build-ui` → `npm run build` в `/web-ui`. Бинарь содержит весь фронт.

**Dev-режим:**
- Go запускается отдельно через `make run-web` (без статики, отдаёт только API).
- Фронт через `npm run dev` в `/web-ui` (Vite-dev-server на `localhost:5173`).
- Vite настроен на проксирование `/api/*` на Go-бэк (`localhost:8000`).

### 17.2 Backend: архитектура и слои

**Clean Architecture** с тремя слоями: `handler → usecase → repository`. Между слоями — интерфейсы; зависимости направлены **только внутрь**: handler знает про usecase, usecase знает про интерфейсы repo, repo не знает ни про что выше себя. Это и есть Dependency Inversion — основное правило Clean.

**Назначение слоёв:**

- **Domain** — самый внутренний слой. Доменные сущности (`Node`, `User`, `Session`, `LogRecord`) как обычные Go-структуры. Доменные ошибки как переменные пакета. **Никаких зависимостей наружу** — ни на pgx, ни на gin, ни на redis. Только стандартная библиотека.
- **Usecase** — слой бизнес-логики. Реализует use cases приложения: «создать узел», «выполнить replay», «выпустить API-токен». Зависит **только от domain и от интерфейсов repo**. Не знает про HTTP, gRPC, JSON-сериализацию, конкретные хранилища.
- **Adapter (in)** — handler'ы для HTTP/gRPC. Парсят входной запрос, валидируют DTO, вызывают usecase, форматируют ответ. Один adapter на тип входа: `adapter/in/http`, `adapter/in/grpc`.
- **Adapter (out)** — конкретные реализации интерфейсов repo. PostgreSQL, Redis, ClickHouse, Kafka — каждое внешнее хранилище живёт в своём подпакете и реализует интерфейс из usecase. Один adapter на хранилище.

**Структура пакета (для Web Service; аналогично для Receiver и Sender):**

```
/internal/web
  /domain                # доменные сущности и ошибки
    node.go              # type Node struct {...}, type NodeStatus
    user.go              # type User struct {...}
    session.go           # type Session struct {...}
    errors.go            # ErrNodeNotFound, ErrPermissionDenied, ...
  /usecase               # бизнес-логика
    node.go              # type NodeUsecase, методы Create/Get/Update/Delete/Replay/DryRun
    auth.go              # type AuthUsecase, методы Login/Logout
    audit.go
    /port                # ИНТЕРФЕЙСЫ для входа в usecase и для выхода в repo
      node_repo.go       # interface NodeRepo, NodeCache
      audit_repo.go      # interface AuditRepo
      session_repo.go    # interface SessionRepo
      clickhouse.go      # interface LogReader, LogWriter
  /adapter
    /in
      /http              # Gin handlers
        node_handler.go
        auth_handler.go
        dto.go           # CreateNodeRequest, NodeResponse и т.п.
        routes.go        # сборка роутера
      /grpc              # (для Receiver: gRPC к Sender)
    /out
      /postgres          # реализации NodeRepo, AuditRepo
        node_repo.go     # type NodeRepoPg, implements port.NodeRepo
        sqlc/            # сгенерированный sqlc-код
      /redis             # реализация SessionRepo, NodeCache
        session_repo.go
        cache.go
      /clickhouse        # реализация LogReader, LogWriter
        log_writer.go
        log_reader.go
      /kafka
        producer.go
        consumer.go
  /middleware            # cross-cutting: auth, ratelimit, audit, recovery, logging
```

Аналогично для `/internal/receiver` и `/internal/sender` — те же слои `domain`, `usecase`, `adapter/in`, `adapter/out`.

**Интерфейсы — основа Clean.** Каждый usecase зависит **только** от интерфейсов, объявленных в `usecase/port`:

```go
// /internal/web/usecase/port/node_repo.go
package port

import (
    "context"
    "github.com/<org>/databus/internal/web/domain"
)

type NodeRepo interface {
    Get(ctx context.Context, id string) (*domain.Node, error)
    GetByPath(ctx context.Context, path string) (*domain.Node, error)
    List(ctx context.Context, filter ListFilter) ([]*domain.Node, error)
    Create(ctx context.Context, node *domain.Node) error
    Update(ctx context.Context, node *domain.Node) error
    Delete(ctx context.Context, id string, dropTable bool) error
}

type NodeCache interface {
    Get(ctx context.Context, id string) (*domain.Node, error)
    Set(ctx context.Context, node *domain.Node, ttl time.Duration) error
    Invalidate(ctx context.Context, id string) error
}
```

Usecase использует эти интерфейсы:

```go
// /internal/web/usecase/node.go
package usecase

type NodeUsecase struct {
    repo   port.NodeRepo
    cache  port.NodeCache
    chRepo port.LogWriter
    audit  port.AuditRepo
    logger *logging.Wrappedlogger
}

func NewNodeUsecase(
    repo port.NodeRepo,
    cache port.NodeCache,
    chRepo port.LogWriter,
    audit port.AuditRepo,
    logger *logging.Wrappedlogger,
) *NodeUsecase {
    return &NodeUsecase{repo: repo, cache: cache, chRepo: chRepo, audit: audit, logger: logger}
}

func (u *NodeUsecase) Get(ctx context.Context, id string) (*domain.Node, error) {
    if node, err := u.cache.Get(ctx, id); err == nil {
        return node, nil
    }
    node, err := u.repo.Get(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("get node: %w", err)
    }
    _ = u.cache.Set(ctx, node, 5*time.Minute)
    return node, nil
}
```

Реализация интерфейса живёт в adapter, реализует тот же контракт:

```go
// /internal/web/adapter/out/postgres/node_repo.go
package postgres

type NodeRepoPg struct {
    queries *sqlc.Queries  // сгенерированный sqlc-код
    logger  *logging.Wrappedlogger
}

func NewNodeRepoPg(pool *pgxpool.Pool, logger *logging.Wrappedlogger) *NodeRepoPg {
    return &NodeRepoPg{queries: sqlc.New(pool), logger: logger}
}

// Реализует port.NodeRepo
func (r *NodeRepoPg) Get(ctx context.Context, id string) (*domain.Node, error) {
    row, err := r.queries.GetNode(ctx, id)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, domain.ErrNodeNotFound
        }
        return nil, fmt.Errorf("queries.GetNode: %w", err)
    }
    return toDomain(row), nil
}
```

`toDomain` — функция-маппер из БД-модели sqlc в доменную модель. Это и есть граница: всё, что выше adapter, не знает про pgx и sqlc.

**Сборка в main.** В `cmd/web/main.go` всё связывается вручную — никаких DI-фреймворков:

```go
func main() {
    cfg := config.Load()
    logger := logging.Initlogger(&cfg.Logging, &cfg.Sentry)

    // Adapters out — конкретные реализации
    pgPool := mustConnectPg(cfg.Postgres)
    redisClient := mustConnectRedis(cfg.Redis)
    chClient := mustConnectClickHouse(cfg.ClickHouse)

    nodeRepo := postgres.NewNodeRepoPg(pgPool, logger)
    nodeCache := redisPkg.NewNodeCache(redisClient, logger)
    auditRepo := postgres.NewAuditRepoPg(pgPool, logger)
    sessionRepo := redisPkg.NewSessionRepo(redisClient, logger)
    chWriter := clickhouse.NewLogWriter(chClient, logger)
    chReader := clickhouse.NewLogReader(chClient, logger)

    // Usecases — принимают интерфейсы
    nodeUsecase := usecase.NewNodeUsecase(nodeRepo, nodeCache, chWriter, auditRepo, logger)
    authUsecase := usecase.NewAuthUsecase(userRepo, sessionRepo, auditRepo, logger)

    // Adapters in
    nodeHandler := httpAdapter.NewNodeHandler(nodeUsecase, logger)
    authHandler := httpAdapter.NewAuthHandler(authUsecase, logger)

    router := gin.New()
    httpAdapter.SetupRoutes(router, nodeHandler, authHandler, /*...middleware*/)

    srv := server.New(cfg.Web, router, logger)
    if err := srv.Run(ctx); err != nil {
        logger.ErrorWithOp("server stopped", err, "main")
        os.Exit(1)
    }
}
```

DI-фреймворки (`wire`, `fx`, `dig`) не используются. Линейный `main.go` на 150–250 строк остаётся читаемым; графы зависимостей через рефлексию или codegen дают сложность без выгоды для проекта этого размера.

**Когда расширять Clean.** Если в каком-то usecase появляется ветвление по типу источника данных (например, «если узел из определённой команды — кешируем по-другому») — это знак выделить ещё один интерфейс. Если интерфейс реализуется только одной структурой и не используется в тестах — это сигнал, что интерфейс пока избыточен. Балансируем между догматизмом и прагматизмом: Clean даёт хорошую структуру, но не превращаем её в самоцель.

**Контекст и cancellation.** Каждая функция, обращающаяся к внешним системам (БД, HTTP, gRPC), первым аргументом принимает `context.Context`. Контекст пробрасывается из handler'а вниз. Любой запрос Receiver'а на 500 rps должен корректно отменяться при closed connection клиента — иначе утечка горутин.

**Обработка ошибок.** Доменные ошибки определяются в `domain/errors.go` как переменные пакета. Усilовия пробрасываются вверх через `fmt.Errorf("%w", err)`:

```go
// /internal/web/domain/errors.go
var (
    ErrNodeNotFound      = errors.New("node not found")
    ErrNodeAlreadyExists = errors.New("node already exists")
    ErrNodePathInvalid   = errors.New("invalid node path")
    ErrPermissionDenied  = errors.New("permission denied")
    ErrLimitReached      = errors.New("node limit reached")
)
```

Handler различает ошибки через `errors.Is` и мапит на HTTP-статусы:

```go
func (h *NodeHandler) Get(c *gin.Context) {
    node, err := h.usecase.Get(c.Request.Context(), c.Param("id"))
    if err != nil {
        switch {
        case errors.Is(err, domain.ErrNodeNotFound):
            c.JSON(404, gin.H{"error": "node not found"})
        case errors.Is(err, domain.ErrPermissionDenied):
            c.JSON(403, gin.H{"error": "permission denied"})
        default:
            h.logger.ErrorWithOp("get node failed", err, "handler.Node.Get",
                h.logger.Str("id", c.Param("id")))
            c.JSON(500, gin.H{"error": "internal error"})
        }
        return
    }
    c.JSON(200, toResponseDTO(node))
}
```

Никаких `panic` в продакшн-коде. Recovery middleware ловит случайные паники и пишет в Sentry, но это аварийный механизм.

**Тестирование слоёв:**
- **Usecase** — основной фокус unit-тестов. Repo и cache подменяются mock'ами (через интерфейсы — это и есть главная польза Clean). Покрытие ≥80% для usecase обязательно.
- **Handler** — table-driven тесты через `httptest.NewRecorder`, usecase в этом тесте — mock или fake.
- **Adapter/out** — integration-тесты через `testcontainers-go`, реальный PostgreSQL/Redis/ClickHouse. Это медленно, но без этого репозиторий не проверишь — sqlc-функции могут сломаться при изменении схемы, и только настоящая БД это поймает.
- **Domain** — pure functions без зависимостей, тривиально покрываются unit-тестами.

### 17.3 Backend: HTTP-слой

**Router:** Gin (`github.com/gin-gonic/gin`). Зрелый, быстрый, известный. Не Echo, не Fiber — Gin уже выбран для Receiver (см. §3), используется и в Web.

**Структура роутера:** регистрация маршрутов одним файлом `routes.go`, никаких авто-discovery через рефлексию:

```go
func SetupRoutes(r *gin.Engine, h *Handlers, mw *Middleware) {
    r.GET("/health", h.Health.Live)
    r.GET("/ready", h.Health.Ready)
    r.GET("/metrics", gin.WrapH(promhttp.Handler()))

    api := r.Group("/api")
    api.POST("/auth/login", h.Auth.Login)

    authed := api.Group("/", mw.Auth.RequireSession())
    authed.POST("/auth/logout", h.Auth.Logout)
    authed.GET("/nodes", h.Nodes.List)
    authed.POST("/nodes", mw.Auth.RequireRole("admin"), h.Nodes.Create)
    // ...
}
```

**Middleware-цепочка** (порядок важен):
1. **Recovery** — ловит panic, пишет в Sentry, возвращает 500.
2. **RequestID** — генерирует `X-Request-Id`, добавляет в context для логирования и Sentry.
3. **Logger** — структурированный лог запроса (метод, путь, статус, длительность) через DI-логгер.
4. **CORS** (только для dev, в проде один origin).
5. **Auth** — проверка session-cookie или API-токена.
6. **RateLimit** — для эндпоинтов с лимитами.
7. **Audit** — для mutating-эндпоинтов записывает действие в `user_audit`.

**Валидация входных DTO:**

```go
type CreateNodeRequest struct {
    Path       string `json:"path" binding:"required,max=255"`
    RootMethod string `json:"root_method" binding:"required,oneof=request requestAsync"`
    TargetURL  string `json:"target_url" binding:"required,url,max=2048"`
}

func (h *NodeHandler) Create(c *gin.Context) {
    var req CreateNodeRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(400, gin.H{"error": err.Error()})
        return
    }
    // ...
}
```

Тег `binding` — встроенный валидатор Gin (на основе `go-playground/validator`). Покрывает 90% случаев. Сложные кейсы (например, валидация allowlist хостов) — внутри usecase через явные проверки доменных инвариантов.

**Маппинг DTO ↔ domain.** Handler работает с DTO (`CreateNodeRequest`, `NodeResponse`), usecase — с доменными моделями (`domain.Node`). Маппинг — функция `toDomain(req CreateNodeRequest) *domain.Node` рядом с DTO, обратный — `toResponseDTO(node *domain.Node) NodeResponse`. Это лишний слой кода, но он защищает: usecase не зависит от формата API, API может меняться без правок usecase, и наоборот.

### 17.4 Backend: репозитории как реализации интерфейсов

В Clean Architecture репозитории — это **реализации** интерфейсов из `usecase/port` (см. §17.2). Они живут в `adapter/out/<storage>` и единственная их роль — превращать вызовы доменного контракта в работу с конкретным хранилищем.

**PostgreSQL:** `pgx/v5` (нативный драйвер) + `sqlc` для генерации Go-кода из SQL. ORM (GORM, ent) **не используются** — добавляют сложность, скрывают SQL, теряют типобезопасность на крайних случаях. SQL пишется руками, генерация даёт типобезопасные функции:

```sql
-- /internal/web/adapter/out/postgres/queries/nodes.sql

-- name: GetNode :one
SELECT * FROM nodes WHERE id = $1;

-- name: ListNodesByTeam :many
SELECT * FROM nodes WHERE team_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: CreateNode :one
INSERT INTO nodes (id, path, root_method, target_url, ...)
VALUES ($1, $2, $3, $4, ...) RETURNING *;
```

`sqlc generate` создаёт `nodes.sql.go` с типизированными функциями `queries.GetNode(ctx, id)`. Конфиг `sqlc.yaml` лежит в репозитории, генерация запускается через `make gen` (часть `make swagger proto sqlc`). sqlc-функции возвращают свои row-структуры — они **внутренние** для adapter'а; наружу через интерфейс уходят только доменные модели.

**Маппинг row → domain.** В каждом adapter-пакете есть `mapper.go`, который превращает row-структуры sqlc в `*domain.Node` (и обратно). Это и есть граница, через которую инфраструктура не просачивается в usecase.

**Транзакции.** В Clean транзакция — это инфраструктурное понятие, его не должно быть в usecase. Решение: интерфейс `port.UnitOfWork`, который запускает transactional-блок:

```go
// /internal/web/usecase/port/uow.go
type UnitOfWork interface {
    Execute(ctx context.Context, fn func(ctx context.Context, repos Repos) error) error
}

type Repos struct {
    Nodes NodeRepo
    Audit AuditRepo
    // ...
}
```

Реализация в `adapter/out/postgres/uow.go` начинает транзакцию pgx, оборачивает все репозитории в их транзакционные версии (то же sqlc, но через `pgx.Tx`), вызывает `fn`, коммитит при успехе или откатывает при ошибке.

Usecase использует это так:

```go
func (u *NodeUsecase) Create(ctx context.Context, node *domain.Node) error {
    return u.uow.Execute(ctx, func(ctx context.Context, repos port.Repos) error {
        if err := repos.Nodes.Create(ctx, node); err != nil {
            return err
        }
        return repos.Audit.Log(ctx, "node.create", node.ID)
    })
}
```

Usecase не знает про `BEGIN`/`COMMIT`/`pgx.Tx` — только про факт «эти операции должны быть атомарны».

**Redis:** `go-redis/v9`. Реализация в `adapter/out/redis`:

```go
// /internal/web/adapter/out/redis/session_repo.go
type SessionRepoRedis struct {
    client *redis.Client
    logger *logging.Wrappedlogger
}

func NewSessionRepoRedis(client *redis.Client, logger *logging.Wrappedlogger) *SessionRepoRedis {
    return &SessionRepoRedis{client: client, logger: logger}
}

// Реализует port.SessionRepo
func (r *SessionRepoRedis) Get(ctx context.Context, token string) (*domain.Session, error) {
    data, err := r.client.Get(ctx, "session:"+token).Bytes()
    if err != nil {
        if errors.Is(err, redis.Nil) {
            return nil, domain.ErrSessionNotFound
        }
        return nil, fmt.Errorf("redis get: %w", err)
    }
    var s domain.Session
    if err := json.Unmarshal(data, &s); err != nil {
        return nil, fmt.Errorf("unmarshal session: %w", err)
    }
    return &s, nil
}
```

Прямых вызовов `redisClient.Get(...)` из usecase нет — только через интерфейс. Это даёт точки для метрик, retry, file-fallback (как у ClickHouse).

**ClickHouse:** официальный `ClickHouse/clickhouse-go/v2`. Запись логов — батчем через `client.PrepareBatch` (см. §4.3). Чтение — обычными запросами через `Query`/`Select`. Адаптер реализует `port.LogWriter` (Receiver/Sender) и `port.LogReader` (Web).

Для UI-поиска по логам с разнообразными фильтрами — query-builder на голом string concat'е допустим (это аналитика, не критичный путь), но с **обязательной параметризацией** значений через `?` (иначе SQL-injection). В контексте Clean это не проблема: query-builder локален для adapter'а ClickHouse, наружу через интерфейс уходит чистый `[]*domain.LogRecord`.

### 17.5 Frontend: стек и структура

**Стек:**
- **React 18 + TypeScript** — индустриальный стандарт, легко найти разработчиков, понятная экосистема.
- **Vite** — dev-server и сборщик. Быстрый HMR (< 100 мс), нативная поддержка TS, простой конфиг.
- **TanStack Query (React Query)** — серверный state, кеширование, инвалидация, refetch. Не Redux — Redux нужен только для сложного клиентского state, которого у нас почти нет.
- **React Router 6** — клиентский роутинг.
- **shadcn/ui + Tailwind CSS** — компонентная библиотека и стилизация. Не Material UI, не Ant Design — shadcn даёт собственные доступные компоненты на основе Radix UI с минимальным runtime.
- **Lucide icons** — иконки (соответствует mockup'ам в этом ТЗ).
- **react-i18next** — локализация (см. §7.11).

**Структура `/web-ui`:**
```
/web-ui
  package.json
  vite.config.ts
  tsconfig.json
  tailwind.config.js
  /public                # статические assets
  /src
    main.tsx             # точка входа
    App.tsx              # корневой компонент с роутером
    /api                 # HTTP-клиент и типы API
      client.ts          # axios или fetch wrapper
      nodes.ts
      auth.ts
      logs.ts
      types.ts           # TypeScript-типы API (из Swagger через openapi-typescript)
    /pages               # страницы (по одной на route)
      Login.tsx
      Overview.tsx
      NodeDetail.tsx
      NodeSettings.tsx
      Settings/
        ClickHouse.tsx
        Users.tsx
        ApiTokens.tsx
        Sentry.tsx
        Language.tsx
      AuditLog.tsx
    /components          # переиспользуемые компоненты
      /ui                # shadcn-компоненты (Button, Input, Dialog, ...)
      NodeCard.tsx
      LogTable.tsx
      LiveTailToggle.tsx
    /hooks               # custom hooks
      useNodes.ts        # TanStack Query queries для nodes
      useLogs.ts
      useLiveTail.ts     # SSE-обёртка
      useAuth.ts
    /lib                 # утилиты
      format.ts          # форматирование дат, чисел, длительности
      validate.ts        # клиентская валидация форм
    /locales             # i18n
      en.json
      ru.json
    /styles
      globals.css
```

**Типы API.** Генерируются из Swagger через `openapi-typescript`:
```bash
npx openapi-typescript http://localhost:8000/swagger/doc.json -o ./src/api/types.ts
```
Это даёт type-safe обращение к API: `client.get<Node>("/api/nodes/" + id)`. Регенерация — часть `make swagger`.

### 17.6 Frontend: паттерны

**Серверный state — через TanStack Query, не useState.**

❌ Так:
```tsx
const [nodes, setNodes] = useState<Node[]>([]);
useEffect(() => {
    fetch("/api/nodes").then(r => r.json()).then(setNodes);
}, []);
```

✅ Так:
```tsx
const { data: nodes, isLoading, error } = useQuery({
    queryKey: ["nodes"],
    queryFn: () => api.nodes.list(),
});
```

TanStack Query сам делает кеширование, refetch при фокусе окна, инвалидацию после mutation, retry на ошибках — заменяет 80% reasons использовать Redux.

**Mutations с инвалидацией:**

```tsx
const queryClient = useQueryClient();
const createNode = useMutation({
    mutationFn: (data: CreateNodeRequest) => api.nodes.create(data),
    onSuccess: () => {
        queryClient.invalidateQueries({ queryKey: ["nodes"] });
        toast.success("Узел создан");
    },
    onError: (err) => toast.error("Ошибка: " + err.message),
});
```

**Live-tail логов через SSE:** отдельный hook `useLiveTail`, который держит EventSource и пушит новые записи в локальный стейт компонента; не использует TanStack Query (Query — для запрос-ответ паттернов, не для стримов).

```tsx
function useLiveTail(nodeId: string, enabled: boolean) {
    const [records, setRecords] = useState<LogRecord[]>([]);
    useEffect(() => {
        if (!enabled) return;
        const es = new EventSource(`/api/nodes/${nodeId}/logs/stream`);
        es.onmessage = (e) => {
            const record = JSON.parse(e.data);
            setRecords(prev => [record, ...prev].slice(0, 500));
        };
        return () => es.close();
    }, [nodeId, enabled]);
    return records;
}
```

**Формы — через react-hook-form + zod.** Никакого ручного управления `useState` на каждое поле:

```tsx
const schema = z.object({
    path: z.string().min(1).max(255).regex(/^[a-zA-Z0-9][a-zA-Z0-9/_-]*$/),
    target_url: z.string().url().max(2048),
});
const { register, handleSubmit, formState: { errors } } = useForm({
    resolver: zodResolver(schema),
});
```

Zod-схемы дублируют backend-валидацию, но это нормально: backend — источник правды, frontend — UX. Лимиты держатся в одном месте через общий JSON или генерацию из OpenAPI.

**Аутентификация в SPA:**
- Cookie с session-токеном устанавливается backend'ом при login (см. §7.1).
- На каждый XHR браузер автоматически шлёт cookie — frontend ничего не делает руками.
- На 401 от любого API-вызова — глобальный interceptor axios/fetch редиректит на `/login`.
- TanStack Query при 401 не делает retry (настройка глобального `retry: false` для 401/403).

**Локализация (i18n):**
- Все строки UI — через `t("namespace.key")`, никаких хардкод-строк.
- JSON-файлы переводов в `/src/locales/{en,ru}.json` с структурой по экранам.
- Текущий язык хранится в Redis-сессии (см. §7.11), на клиенте — через `i18next` инициализируется из ответа `/api/auth/me`.

**Тестирование фронта:**
- **Vitest** для unit-тестов hooks и утилит. Быстрее Jest, нативная интеграция с Vite.
- **React Testing Library** для компонентов.
- **Playwright** для e2e — минимальный набор happy-path тестов (логин, создание узла, просмотр логов). Запускается отдельной целью `make test-e2e` против поднятого `docker-compose`.

### 17.7 Что не используем

- **GORM, ent** — скрывают SQL, добавляют сложности.
- **Wire, fx, dig (DI-фреймворки)** — для нашего размера ручной DI в `main.go` понятнее.
- **Redux, MobX, Zustand** — серверный state покрывает TanStack Query, клиентского state мало (текущая вкладка, фильтры) — local state хватает.
- **Server-Side Rendering / Next.js** — это админка, SEO не нужен, SSR добавляет сложность без выгоды.
- **GraphQL** — REST + Swagger закрывают потребности; GraphQL дал бы пользу при N клиентах с разными нуждами, у нас один клиент (UI).
- **gqlgen, twirp** — для внутреннего gRPC хватает стандартного `protoc-gen-go-grpc`.
- **Любые in-house велосипеды на роутинг / валидацию / DI** — используем зрелые библиотеки.

### 17.8 Что включить в обязательные code review правила

- Любая функция, обращающаяся к внешним системам, **обязана** принимать `context.Context` первым аргументом.
- Ошибки **обязаны** обёртываться через `%w` при пробросе вверх (`fmt.Errorf("get node: %w", err)`), а не теряться (`return err`) — иначе невозможно построить цепочку через `errors.Is`.
- Логирование ошибок **обязано** идти через `ErrorWithOp` с указанием `op` (см. §14.1).
- HTTP-handler **не делает** напрямую запросы к БД — только через usecase.
- **Usecase зависит только от интерфейсов в `port/`**, не от конкретных реализаций (`*postgres.NodeRepoPg`). Нарушение = нарушение Dependency Inversion.
- **Domain не зависит ни от чего внешнего** — ни pgx, ни gin, ни redis, ни logging-фреймворка. Только стандартная библиотека.
- **Adapter не вызывает другой adapter напрямую** — только через интерфейс из `port/`, который реализован в usecase или передан явно. Иначе разрушается изоляция слоёв.
- DTO в handler ↔ доменная модель в usecase — **обязательный маппинг** через `toDomain` / `toResponseDTO`. Нельзя возвращать `*domain.Node` напрямую как JSON-ответ.
- SQL-запросы **только параметризованные** (`$1`, `$2`) — никакой string concatenation с user input.
- React-компонент **не делает** прямых fetch-вызовов — только через hook на TanStack Query.
- Любая новая зависимость в `go.mod` или `package.json` — обсуждается на code review (защита от dependency hell).
