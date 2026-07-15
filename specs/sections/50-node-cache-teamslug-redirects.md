# 50. Кеш узла по реальной команде + видимость редиректов Sender

Раздел закрывает боевой баг «правки узла не применяются до 5 минут» и добавляет видимость 3xx-редиректов,
за которыми молча следует Sender. Развивает §9.2 (горячий кеш конфига узла), §18 (multi-tenancy) и §22/§42
(логи узла).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 50.1 Баг: ключ кеша не по той команде

Горячий кеш конфига узла в Redis (§9.2) имеет ключ `node:<team_slug>:<path>`. Receiver читает именно его
([nodecache/reader.go](../../internal/receiver/adapter/out/nodecache/reader.go) `nodeKey`). Но Web писал и
инвалидировал ключ **всегда** как `node:default:<path>` — хардкод `DefaultTeamSlug`, оставшийся с v1 («в v1
команда всегда default»). После §18 (multi-tenancy) `path` уникален только внутри команды, и для любого узла
**вне команды `default`** write-through и инвалидация из Web **промахивались мимо ключа Receiver**.

**Симптом (боевой):** в `target_url` узла команды `task` поменяли `http://…`, но Sender ещё ~5 минут ходил на
старый `https://…` и падал на TLS (`x509 … Vozovoz Root CA`). Через `redis.node_ttl_sec` (до 5 мин) ключ
протухал, Receiver перечитывал из PG — и адрес становился верным. То есть до истечения TTL правки не доходили.

**Масштаб шире `target_url`:** для узла вне `default` до TTL не применялись смена авторизации/таймаутов,
**выключение/пауза**, **удаление** (`InvalidateByPath` тоже промахивался — удалённый узел продолжал
обслуживать трафик) и **перенос** между командами.

## 50.2 Фикс: неймспейс ключа по реальному slug + шифрование кредов

- `port.NodeCache` получает `teamSlug` первым смысловым аргументом во всех методах
  ([port/node_repo.go](../../internal/web/usecase/port/node_repo.go)).
- Адаптер [node_cache.go](../../internal/web/adapter/out/redis/node_cache.go): `nodeKey(teamSlug, path)`
  (пустой → `DefaultTeamSlug`), совпадает с ключом Receiver'а.
- **Креды в кеше шифруются** тем же AES-256-GCM, что и в PostgreSQL. Receiver читает кеш и делает `Decrypt`;
  раньше Web клал plaintext, и Receiver трактовал бы такую запись как cache-miss. Конструктор
  `NewNodeCacheRedis` принимает `*crypto.Cipher`.
- Usecase [node.go](../../internal/web/usecase/node.go): `resolveCacheTeamSlug(teams, teamID)` резолвит
  `team_id → slug` через `teams.GetByID`; **пустой slug = не трогать кеш** (нет TeamRepo / команда не нашлась —
  лучше подождать TTL, чем затереть ключ чужой команды). Хелперы `cacheSet`/`cacheInvalidate` применены в
  Create/Update/SetStatus/Delete/Move. То же в [host_allowlist.go](../../internal/web/usecase/host_allowlist.go)
  (`refreshCache`; конструктор получил `teams port.TeamRepo`).

**Грабли деплоя:** формат кредов в кеше меняется (plaintext → шифр). После выката старые записи `node:default:*`
у Receiver не расшифруются → трактуются как cache-miss с перечиткой из PG (TTL короткий) — безопасно; в логах
Receiver заметно всплеском `decrypt cached auth`.

## 50.3 Видимость 3xx-редиректов Sender

HTTP-клиент Sender ([httpclient/client.go](../../internal/sender/adapter/out/httpclient/client.go)) следовал
редиректам молча (дефолт Go, `CheckRedirect` не задан). Проблемы: (1) фактический адрес отличается от
`target_url` (напр. узел с `http://` target, а сервер отдаёт `301 → https`), и это не видно; (2) при
`301/302/303` POST превращается в GET и **тело запроса молча теряется**.

Решение — **следование оставлено** (лимит 10 хопов как у Go), добавлена видимость:

- На каждый вызов — дешёвая копия `http.Client` (Transport общий, пул соединений не рвётся) с замыканием
  `CheckRedirect`, которое логирует хоп и накапливает его в `HTTPResponse.Redirects`.
- **Уровень служебного лога:** `Info` — обычный переход; `Warn` — смена метода (`POST→GET`, тело потеряно —
  реальный риск). Только `Error` уходит в Sentry → флуда нет.
- **Где видит пользователь:** факт редиректа дописывается в `reason` записи лога узла
  (`appendRedirectNote` в [send.go](../../internal/sender/usecase/send.go)) → виден в UI в блоке «Причина»
  детали строки лога. **Одна запись лога на запрос** — редиректы происходят внутри одного `http.Client.Do`,
  новых строк ClickHouse и роста счётчиков §44 нет.
- **Безопасность URL:** `redactURL` отдаёт `scheme://host/path` без query (в query бывают токены; ни
  slog-хелперы, ни Sentry query не маскируют — зеркалит `otel.sanitizeURL`).
- `port.HTTPRequest.NodePath` добавлен только ради поля `node=` в служебном логе (на HTTP-вызов не влияет).

## 50.x Scope (что не входит)

- Запрет/ограничение редиректов на стороне Sender (`CheckRedirect` → отдавать 3xx как есть) — сознательно
  НЕ делаем: сломало бы узлы с `http`-target, реально редиректящим на `https`. Оставляем следование + видимость.
- Метрика Prometheus по редиректам — возможный follow-up (сейчас только лог).
- Прогрев/предзагрузка кеша при старте — вне scope; полагаемся на write-through + TTL.
