---
name: nexus-prod
description: Анализ боевого Nexus (nexus.vz78.vozovoz.ru) по REST API — снять и сверить счётчики дашборда, разобрать расхождения шапки/таблицы, прочитать метрики/логи/аудит узлов. Use when investigating production counters, reconciling Prometheus↔ClickHouse, or diagnosing a live Nexus dashboard discrepancy.
---

# Nexus — анализ боевого инстанса по API

Боевой Nexus отдаёт REST под `/api/*` (тот же Web Service, что и SPA). Этот скил —
как подключиться к бою токеном и **сверить счётчики** (шапка дашборда vs таблица узлов vs
деталь узла), чтобы объяснить расхождения. Контракт расчёта счётчиков — раздел ТЗ
[§44](../../../specs/sections/44-dashboard-counters.md); карта реализации —
[IMPLEMENTATION.md](../../../specs/IMPLEMENTATION.md).

## 1. Подключение

- **Base:** `https://nexus.vz78.vozovoz.ru`, API base `…/api`.
- **Токен (Bearer):** в памяти — `[memory] reference_nexus_prod_api` (значение `db_…` НЕ хранится в
  репозитории). Заголовок `Authorization: Bearer db_…`. Если токен протух / не хватает scope —
  попроси новый у пользователя и обнови memory.
- **Scope ≠ роль.** У API-токена СВОИ scope, независимые от роли пользователя. Нужны
  `metrics:read` (метрики), `nodes:read` (список узлов), `logs:read` (логи), `audit:read` (аудит).
  Различение: нет токена → **401**; валидный токен без нужного scope → **403** (тело пустое). Так
  можно проверить, какие scope есть.
- **PowerShell (Windows-стенд разработчика):** включи TLS1.2 и проставь User-Agent:

```powershell
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$h = @{ Authorization = "Bearer $env:NEXUS_PROD_TOKEN"; "User-Agent" = "Mozilla/5.0" }
$base = "https://nexus.vz78.vozovoz.ru"
Invoke-RestMethod -Uri "$base/api/metrics/overview" -Headers $h -TimeoutSec 25
```

`/api/version` доступен без scope — быстрый чек доступности и версии.

## 2. Карта endpoint'ов для анализа

| Endpoint | Что отдаёт | Scope |
|---|---|---|
| `GET /api/version` | `{version, commit, build_date}` | — |
| `GET /api/auth/me` | текущий пользователь токена (login/role/current_team_id) | — |
| `GET /api/metrics/overview` | KPI шапки: **только** `kafka_queue` + `prometheus_available` (§44.A) | metrics:read |
| `GET /api/metrics/nodes?range=1h\|3h\|24h\|7d\|14d\|30d` | per-node throughput (CH, уникальные) + `totals` для шапки | metrics:read |
| `GET /api/metrics/nodes/{id}?range=…` | KPI узла (`total/delivered/errors/p95/p99`) — эталон сверки | metrics:read |
| `GET /api/metrics/diagnostics?range=…` | **сверка** Prometheus(попытки) ↔ ClickHouse(уникальные) + per-node + дельты (§44.E) | metrics:read |
| `GET /api/nodes` | список узлов (id/path/root_method/status) | nodes:read |
| `GET /api/nodes/{id}/logs/failed-count` | дешёвый счётчик недоставленных | logs:read |
| `GET /api/audit?limit=N` | аудит-лог | audit:read |

`from`/`to` (RFC3339 или UnixMilli) вместо `range` — произвольный календарный период.

## 3. Контракт счётчиков (НЕ перепутать источники)

После §44.A **шапка = Σ строк таблицы** за выбранный период (один источник — ClickHouse, уникальные
запросы). До фикса шапка считалась из Prometheus (попытки) и не сходилась. Сейчас:

- **Таблица / шапка-totals (ClickHouse, уникальные запросы):** `In=countDistinct(ID)`,
  `Out=uniqExactIf(ID,done=1)`, `Errors=In−Out`. Всегда `Out ≤ In`. error_rate = `errors/incoming`.
- **Деталь узла** `/api/metrics/nodes/{id}` (`total/delivered/errors`) **= строке таблицы** того же
  узла — эталон.
- **Prometheus (попытки, `increase`)** — теперь только для `kafka_queue` в шапке и для
  `/api/metrics/diagnostics`. Считает каждую попытку: ретраи async раздувают исходящие, поэтому в
  Prometheus возможно `outgoing > incoming`.

## 4. Методология сверки (чек-лист)

1. **Снять три среза за один период (например 24ч):** `overview` (kafka), `nodes?range=24h`
   (totals + строки), `nodes/{busiest_id}?range=24h` (KPI узла).
2. **Σ строк таблицы == totals == шапка?** Должно совпадать по построению (§44.A). Если нет —
   баг в агрегации `sumTotals` или фронт читает не из `totals`.
3. **KPI узла == строка таблицы того же узла?** Должно (оба из CH NodeKPI). Если нет — разные окна
   или фильтры.
4. **`/api/metrics/diagnostics`** — посмотреть дельты Prometheus↔ClickHouse и убедиться, что
   расхождение объясняется ожидаемо:
   - `Prometheus.outgoing > Prometheus.incoming` — **ретраи** (попытки), это норма.
   - `ClickHouse.out ≤ ClickHouse.in` — уникальные, норма.
   - `ClickHouse.errors ≥ Prometheus.errors` — CH «не доставлено» включает 3xx/висящие, а
     Prometheus-regex ошибок `0|[45]..` их не считает.
   - `Prometheus < ClickHouse` по входящим — `increase()` занижает (экстраполяция + сброс счётчиков
     при рестартах) против точного `countDistinct`.
5. **Узлы только в Prometheus** (в `diagnostics.nodes` без CH-значений) — orphan/удалённые узлы или
   трафик к несконфигурированным путям; в таблицу (только сконфигурированные узлы) не попадают.

## 5. Гипотезы проблем со счётчиками (что проверять)

- «Шапка ≠ сумма таблицы» → после §44.A не должно быть; если есть — фронт читает старый
  `/api/metrics/overview` incoming/outgoing вместо `nodes.totals`, либо разные периоды.
- «Исходящих больше входящих» в diagnostics-Prometheus → ретраи (норма), не баг.
- «У узла ошибки, но в шапке 0%» → проверь, что error_rate берётся из totals (errors/incoming), а не
  из Prometheus.
- «Пусто/нули везде» → проверь `prometheus_available`/`clickhouse_available`; нестабильный
  react-query queryKey; недоступность CH (file-fallback).

## 6. Безопасность

- Токен — секрет: бери из памяти/`$env:NEXUS_PROD_TOKEN`, **не коммить** в репозиторий и не
  печатай в отчётах целиком.
- Только **read-only GET** для анализа; не дёргай мутации на бою без явной просьбы пользователя.
