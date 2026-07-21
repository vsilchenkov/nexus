# 62. Глобальный поиск узлов + история поиска

Раздел вводит **глобальный поиск узлов** во всех командах пользователя из шапки приложения и
**историю поиска** (последние 10 запросов, персонально на пользователя, в PostgreSQL), общую для
глобального поиска и поля «Поиск» на странице узлов. Развивает §18 (multi-tenancy, команды и
team-switcher), §26 (RBAC), §49 (персональные данные пользователя в PG — избранные команды),
§54 (сохранение фильтров Overview), §58 (авто-переключение команды при открытии узла).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 62.1 Проблема

Поиск узлов на странице узлов (Overview) скоупится **текущей командой** сессии: `GET /api/nodes`
жёстко фильтрует `WHERE team_id = <current_team>` (§18). Чтобы найти узел в другой команде,
пользователю нужно сначала переключить команду в шапке, а он часто не помнит, в какой команде узел.
Плюс введённый поиск нигде не запоминался: набранную строку приходилось вводить заново.

## 62.2 Глобальный поиск в шапке (п.1)

Поле ввода по центру шапки (между хлебными крошками и переключателем команды). Ищет узлы **во всех
командах, в которых состоит пользователь**, показывает найденные с бейджем команды-владельца; при
выборе — переход на страницу узла с авто-переключением сессии на его команду.

### Эндпоинт `GET /api/search/nodes`

- Параметры: `q` (строка, ≥ 2 рун), `limit` (дефолт 20, максимум 50).
- Авторизация: **только session-cookie** (`RequireSessionOnly`). API-токены привязаны к одной команде
  (`token.team_id`), кросс-командная выдача им бессмысленна — та же логика, что у `/nodes/{id}/team`
  (§58) и `/me/switch-team` (§18).
- Ответ:
  ```json
  { "items": [
    { "id": "<uuid>", "path": "svc/parcel", "target_url": "https://…",
      "root_method": "request", "status": "enabled",
      "team_id": "<uuid>", "team_slug": "alpha", "team_name": "Alpha" }
  ] }
  ```
- `q` короче 2 рун (после `TrimSpace`) → `200 { "items": [] }` (не ошибка — фронту проще).
- Реализация: usecase `NodeUsecase.SearchAcrossTeams` обходит членства пользователя
  (`TeamRepo.ListUserTeams`, как `ResolveTeam` §58), передаёт их набор в репозиторий узлов
  (`ListNodesFilter.TeamIDs` → SQL `WHERE team_id = ANY($1)`), ILIKE-поиск по `path`/`target_url`
  переиспользуется как есть. Имена команд обогащаются из уже полученного списка членств (map
  `id → slug/name`), без дополнительных запросов.
- **Почему префикс `/api/search/*`, а не `/api/nodes/search`:** статический сегмент `search`
  конфликтовал бы с wildcard-параметром `:id` из `/api/nodes/:id` в gin-роутере (тот же приём, что с
  `log` vs `logs` в §7.4). Префикс `/search/*` оставлен расширяемым (будущий поиск по логам и т.п.).

### UX поля поиска

- Дропдаун: при **пустом** вводе — история поиска (§62.4), при вводе (≥ 2 рун) — результаты с
  бейджем команды.
- Debounce запроса 300 мс (как поле Overview, §54.4) — бэкенд не дёргается на каждый keystroke.
- Выбор результата = переход `navigate('/nodes/{id}')`; **команду сессии переключает существующий
  `useEnsureNodeTeam`** (§58) — отдельный `switchTeam` не нужен. Введённая строка **не очищается**
  (пользователь может искать ещё раз).
- Введённая строка **переживает уход со страницы и «Назад»**: хранится в `sessionStorage`
  (`nexus.globalsearch.q`), а не в URL — топбар глобален, URL принадлежит страницам (в отличие от
  фильтров Overview §54, которые живут в URL).
- Бонус: хоткей `/` или `Ctrl`/`⌘`+`K` фокусирует поле (если фокус не в поле ввода).

## 62.3 Кросс-командная видимость и RBAC

Поиск ограничен членствами пользователя: узлы команд, в которых он не состоит, в выдачу не попадают
(проверено integration-тестом A/B/C: член A и B → только узлы A и B). Роль (`viewer`/`manager`/`admin`)
видимость **не расширяет** — как и в остальном приложении, видны узлы только своих команд (§18/§26).

## 62.4 История поиска (п.2)

Последние **10** поисковых строк на пользователя, **общие** для глобального поиска и поля «Поиск» на
странице узлов. Строго персональные — не пересекаются между пользователями.

### Модель данных

Таблица `user_search_history` (миграция 0025):

```sql
CREATE TABLE user_search_history (
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    query       TEXT        NOT NULL,
    searched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, query),
    CONSTRAINT user_search_history_query_len CHECK (char_length(query) BETWEEN 1 AND 200)
);
CREATE INDEX user_search_history_recent_idx ON user_search_history (user_id, searched_at DESC);
```

- Хранение в **PostgreSQL** (образец §49 `user_team_favorites`), а не в web-storage: история должна
  быть строго per-user и переживать смену браузера/устройства. Прямой FK на `users(id)` (не составной
  на `user_teams`, как в §49): история привязана к пользователю, запросы кросс-командные.
- **Одна общая история** (без scope-колонки): оба поля ищут в одном домене (узлы по `path`/`target_url`),
  общий список полезен (строка, набранная на Overview, доступна в шапке и наоборот).
- **Дедуп:** `PK (user_id, query)` + `ON CONFLICT DO UPDATE SET searched_at = now()` — повтор той же
  строки всплывает наверх, без дублей. Сравнение регистрозависимое (переиспользуется точная строка
  пользователя; сам ILIKE-поиск регистронезависим).
- **Обрезка до 10** — server-side `DELETE … WHERE query NOT IN (SELECT … ORDER BY searched_at DESC
  LIMIT 10)` в той же транзакции, что и upsert.
- **Нормализация в usecase:** `TrimSpace`; строка короче 2 или длиннее 200 рун — молча игнорируется
  (no-op, а не ошибка): триггеры записи высокочастотные, лишний шум клиенту не нужен.
- **Без audit-записи:** в отличие от одноразовой настройки §49, запись истории — высокочастотное
  self-service действие, зашумило бы журнал.

### API истории (все — `RequireSessionOnly`, `user_id` строго из сессии)

- `GET /api/me/search-history` → `{ "items": ["строка", …] }` (`searched_at DESC`, ≤ 10). Никогда не
  ошибка: при сбое деградирует в пустой список (как `FavoriteTeamIDs` §49).
- `POST /api/me/search-history { "q": "строка" }` → `204` (upsert + trim; мусор тоже `204` — no-op).
- `DELETE /api/me/search-history` → `204` (очистить всю историю — кнопка «Очистить историю» в дропдауне).
  Удаление одиночной записи вне scope: cap 10 сам вымывает старое.

### Триггеры записи (чтобы не копить префиксы «no», «nod», «node»)

- **Глобальный поиск:** при выборе результата (Enter/клик по подсвеченному пункту).
- **Поле Overview:** по Enter в поле и по blur непустого поля (≥ 2 рун). Сервер дедупит — повторы
  безвредны. Клик по пункту истории запись «на blur» гасит (не сохраняет начатую-но-не-завершённую
  строку вместо выбранной).

## 62.5 Реализация (файлы)

Backend: миграция `migrations/0025_user_search_history.{up,down}.sql`; порт
`internal/web/usecase/port/search_history_repo.go` + импл на `UserRepoPg`
(`adapter/out/postgres/user_search_history.go`); `AuthUsecase.WithSearchHistory` +
`SearchHistory`/`RecordSearch`/`ClearSearchHistory` (`usecase/auth.go`); фильтр
`ListNodesFilter.TeamIDs` + `NodeRepo.List` (ANY); `NodeUsecase.SearchAcrossTeams` (`usecase/node.go`);
handlers в `auth_handler.go`/`node_handler.go`; маршруты в `routes.go`; wiring в `app.go`.

Frontend: `web-ui/src/lib/searchHistory.ts` (хуки истории + поиска + persistence `q`);
`web-ui/src/components/GlobalSearch.tsx` (поле в шапке); `web-ui/src/components/SearchHistoryList.tsx`
(история для Overview); врезка в `Topbar.tsx`; интеграция в `pages/Overview.tsx`; ключ
`node-search`/`search-history` в `TEAM_INDEPENDENT_KEYS` (`lib/teams.ts`); i18n namespace `search`
(`locales/{en,ru}.json`).

## 62.6 Вне scope

- Удаление одиночной записи истории (только очистка всей).
- Scope-колонка истории (сейчас одна общая) — при необходимости добавляется колонкой со значением по
  умолчанию без ломки.
- Поиск по атрибутам узла, кроме `path`/`target_url` (комментарий, заголовки и т.п.).
- Фасеты/фильтры в глобальном поиске (метод, статус) — только текстовый поиск.
