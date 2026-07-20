## 41. Универсальная динамическая авторизация + умный Bearer + пересмотр статуса «Down»

До §41 динамическая авторизация (извлечение креды из запроса) была только **исходящей**
(`token_from_request` / `basic_from_request`, §3.5), её источник/поле **не выводились в UI** (молча
применялись дефолты `query`/`token`), пустой токен жёстко отдавал **401**, а конкатенация `"Bearer " +
token` была безусловной (удвоение при значении `Bearer <jwt>`). Входящая авторизация (`token`/`basic`,
§3.3) читала только заголовок `Authorization`. §41 делает механизм универсальным для входа и выхода,
вводит каталог «полей запроса», умный дедуп схемы и мягкую обработку пустого поля. Отдельно §41
переопределяет семантику статуса **«Down»** в Overview.

### 41.1 Источник + поле для входа и выхода

- **Исходящая** (`token_from_request` / `basic_from_request`): источник `auth_dynamic_source`
  (`header` / `query`; legacy `body` сохранён в БД/коде для существующих узлов, но в новом UI не
  предлагается) и имя поля `auth_dynamic_field` теперь редактируются в форме узла. `basic_from_request`
  раньше игнорировал source/field (хардкод заголовка `Authorization`) — теперь honored их; миграция
  пинит существующие строки на `header`/`Authorization` (см. 41.5).
- **Входящая** (`token` / `basic`): новые колонки `incoming_auth_dynamic_source` (`header`/`query`,
  дефолт `header`) и `incoming_auth_dynamic_field` (дефолт `Authorization`). `source=header` сохраняет
  прежний контракт (схема `Bearer`/`Basic` в значении заголовка); `source=query` берёт значение
  параметра напрямую (токен — как есть; basic — `base64(login:password)` без схемы). Сравнение —
  constant-time. Это read-only гейт: query при входящей валидации **не вырезается** (вырезание —
  забота исходящего пути).
- **Каталог полей запроса** (`request_fields_catalog`, §24-подобный): общий справочник имён
  заголовков/параметров для combobox-автодополнения. `usage_count` считается on-read по `COUNT` узлов с
  этим именем в `auth_dynamic_field` **или** `incoming_auth_dynamic_field`. `GET /api/request-fields`
  (любая сессия), `POST` (manager+, идемпотентно по case-insensitive имени).
- **Обязательность поля.** Для исходящих `*_from_request` и для входящих `token`/`basic` при
  `source=query` поле обязательно — фронт блокирует сохранение (`node.validation.auth_field_required`).
  Это UX-гейт: на бэкенде поле всегда имеет дефолт (`SetDefaults`), валидация домена толерантна к
  пустому (пусто = дефолт `header`/`Authorization`).

### 41.2 Умный Bearer и пустое поле (исходящая)

- **Умный дедуп схемы.** Извлечённое значение оборачивается схемой `withScheme`: если оно уже начинается
  с `"Bearer "` / `"Basic "` (регистронезависимо) — берётся **как есть**, иначе добавляется префикс.
  Закрывает реальный кейс `?Bearer=Bearer+<jwt>` (был бы `Bearer Bearer <jwt>`). Делает ручной
  `auth_dynamic_strip_prefix` ненужным в новом UI (колонка оставлена для back-compat, применяется до
  дедупа для header-источника).
- **Пустое/отсутствующее поле → без `Authorization`.** Исходящий путь возвращает пустой заголовок и
  `nil`-ошибку (не 401): запрос уходит в приёмник без авторизации. Sender пустой `Authorization` не
  ставит. Это **асимметрия**: для входящей валидации пустое поле — **401** (гейт сохраняется).

### 41.3 Статус «Down» = по последнему запросу

> **Пересмотрено §52:** булев флаг заменён трёхсостояньем ok/degraded/down
> (4xx больше не красит узел в Down — жёлтый «Degraded»); gauge расширен до
> значений 0/1/2. См. [52-node-degraded-status.md](52-node-degraded-status.md).

До §41 «Down» = `errors/out > 30%` за период (`errors` = Sender `status 0/4xx/5xx`). §41 переопределяет:
узел «**Down**», если **последний** исходящий вызов завершился ошибкой (`status 0/4xx/5xx`); если
последний прошёл (2xx) — приёмник доступен (не Down). Снимок «сейчас», независимый от выбранного окна
(колонки `in/out/errors` остаются за период). Источник — новый gauge
`nexus_node_last_request_error{node}` (Sender выставляет на каждом завершённом вызове), Web читает
`max by(node)` instant-запросом и накладывает флаг `last_error` на строки Overview поверх любой ветки
(CH-источник gauge не считает). Нет данных gauge → не Down (`unknown`/ok, как при `!ready`).

### 41.4 Где применяется

- `internal/domain/enums.go` — `IncomingAuthSource` (`header`/`query`); `node.go` — поля
  `IncomingAuthDynamicSource/Field`, бэкфилл в `SetDefaults` (зависит от режима: `basic_from_request` →
  `header`/`Authorization`, иначе `query`/`token`), валидация; `errors.go` — sentinels.
- `internal/receiver/usecase/auth.go` — `CheckIncomingAuth(node, h, q, body)` + `incomingAuthValue` /
  `checkIncomingToken` / `checkIncomingBasic`; плагирование query в `route.go`, `route_async.go`,
  `dry_run.go`.
- `internal/receiver/usecase/auth_dynamic.go` — `buildFromRequest` + `extractDynamicValue` + `withScheme`
  (умный дедуп, пусто→`Header=""`, basic по source/field).
- Каталог: `internal/domain/request_field_catalog.go`, `internal/web/usecase/request_field_catalog.go`
  (+ port), `internal/web/adapter/out/postgres/request_field_catalog_repo.go`,
  `internal/web/adapter/in/http/request_field_catalog_handler.go`, routes + wiring `app.go`.
- «Down»: `internal/platform/metrics/metrics.go` (gauge `NodeLastRequestError` +
  `SetNodeLastRequestError`), Sender (`sender_service.go` sync + `async.go`),
  `internal/web/adapter/out/prometheus/client.go` (`NodeLastErrors`), `internal/web/usecase/metrics.go`
  (`applyLastErrors` overlay), DTO `last_error`; фронт `Overview.tsx` `nodeVariant`.
- UI: `web-ui/src/components/node/RequestFieldField.tsx` (single-select required combobox),
  `NodeSettings.tsx` (Select источника + picker для входа/выхода), `nodeValidation.ts`.

### 41.5 Хранение, API, миграции

- Миграция `0021_incoming_auth_dynamic` — колонки `incoming_auth_dynamic_source` (CHECK `header|query`,
  дефолт `header`) и `incoming_auth_dynamic_field` (формат как `auth_dynamic_field`, дефолт
  `Authorization`); **data-fix**: существующие `basic_from_request` пинятся на `header`/`Authorization`
  (старый код всегда читал `Authorization`; без пина source/field-aware код читал бы query `token`).
- Миграция `0022_request_fields_catalog` — таблица каталога (`UNIQUE(lower(name))`, имя 1..64, формат
  `^[a-zA-Z][a-zA-Z0-9_-]*$`).
- API: DTO узла `incoming_auth_dynamic_source` (`oneof header query`) / `incoming_auth_dynamic_field`
  (plaintext, не шифруются — это имена полей, не секреты); `/api/request-fields` (GET/POST);
  `nodeThroughputDTO.last_error`. Метрика `nexus_node_last_request_error{node}`.

### 41.7 Basic-креды: Логин/Пароль в UI + `auth_login` в API

- **Хранение не меняется:** креды basic — одна строка `"login:password"` (plaintext в домене,
  AES-256-GCM в БД); Receiver сравнивает/кодирует целую строку. Разделение — только на границе UI/API.
- **API отдаёт логин** (не секрет, в отличие от пароля): `NodeResponse.auth_login` /
  `incoming_auth_login` (omitempty) — часть кредов до **первого** `:` (пароль может содержать
  двоеточия), только при типе `basic`; для `token`/`webhook_signature` не отдаётся (там креды — сам
  секрет). Легаси-креды без `:` трактуются как логин без пароля. Попадает и в list-ответ `/api/nodes`.
- **Форма редактирования:** при типе `basic` (вход. и исход.) вместо одного SecretInput — два поля:
  Логин (обычный Input, prefill из API) + Пароль (SecretInput, всегда пуст; placeholder «оставьте
  пустым, чтобы не менять»). Перед отправкой пара склеивается в `auth_credentials` /
  `incoming_auth_credentials`; пустой пароль → пустые креды = «оставить старые» (контракт Update
  прежний). Валидация: `:` в логине запрещён (`node.validation.login_colon`); **смена логина требует
  ввода пароля заново** (`node.validation.password_required_on_login_change`) — сервер не может
  пересобрать строку, не зная пароля.
- **Просмотр узла (ConfigTab):** вместо одной строки «Авторизация» (только исходящий тип) — две:
  «Входящая авторизация» (скрыта для pull-узлов и типа `none`) и «Исходящая авторизация», каждая —
  тип + логин при `basic`.

### 41.6 Scope и неочевидности

**В scope:** источник+поле для входа и выхода; каталог полей; обязательность поля (UX); умный Bearer/
Basic дедуп; пусто→без auth (исход) / 401 (вход); «Down» по последнему вызову.

- **Почему входящая расширяет `token`/`basic`, а не вводит `*_from_request`:** для входа нет «проброса»,
  только чтение креды для сравнения — `source/field` параметризуют существующие режимы, дефолты
  `header`/`Authorization` сохраняют поведение всех существующих узлов и старого кэш-JSON в Redis.
- **Маскирование:** новые колонки — это **имена** полей (конфиг), не секреты. Исходящий путь вырезает
  значение креды из проксируемого запроса; входящая query-креда не вырезается (она в параметре самого
  клиента). Новых секретов в логи не добавляется; `sentry.go sensitiveKeys` уже маскирует
  `authorization`/`token`.
- **`body` для входящей не предлагается** (нет гейт-кейса); для исходящей `body` остаётся в коде/БД для
  существующих узлов, но в новом UI скрыт.
- **«Down» в CH-ветке Overview:** gauge живёт только в Prometheus, поэтому флаг накладывается общим
  overlay в `NodesOverview` поверх обеих веток; при отсутствии Prometheus деградирует (узлы не красятся).
- **Мульти-реплики Sender:** gauge per-(node,instance); Web берёт `max by(node)` — узел «Down», если у
  любой реплики последний вызов был ошибкой (приближение «глобального последнего»). На одиночном Sender
  (стенд) — точно.

**Out of scope:** OAuth2/mTLS и прочие схемы; перенос исходящего `body`-источника в UI; «глобально
точный последний вызов» через распределённый стор (для v1 достаточно gauge + `max by(node)`).
