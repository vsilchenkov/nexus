## 29. Комментарий узла (описание для команды)

### 29.1. Зачем

При десятках узлов трудно понять назначение каждого: с какой системой интеграция,
какие события идут, кто отвечает, есть ли известные нюансы. Поле «комментарий» —
простое текстовое описание узла для внутреннего использования командой. Это
UI-метаданные: в маршрутизации запросов не участвует, внешним клиентам не отдаётся.

### 29.2. Модель данных

Колонка `comment` в таблице `nodes`:

- тип `TEXT NOT NULL DEFAULT ''` (без указателей в Go, как `webhook_signature_header`);
- ограничение `CHECK (length(comment) <= 2000)` — лимит по символам (code points);
- миграция `0016_node_comment` (up/down).

Домен: `domain.Node.Comment string`. Валидация в `Node.Validate()` — по рунам
(`utf8.RuneCountInString(comment) <= 2000`), чтобы совпадать с PG-CHECK и с
DTO-binding (validator `max` для строк считает руны) — без расхождений для кириллицы.
Ошибка — `ErrNodeCommentLength`.

### 29.3. API

- `CreateNodeRequest.comment` / `UpdateNodeRequest.comment` (`binding:"omitempty,max=2000"`).
- `NodeResponse.comment`.
- Мапперы `reqToDomain` / `nodeToResponse` пробрасывают поле как обычную строку.
- Swagger перегенерирован (`make swagger`).

Пример:

```json
POST /api/nodes
{
  "path": "webhook/send",
  "root_method": "request",
  "target_url": "https://api.example.com/hook",
  "comment": "Интеграция с системой заказов. Шлёт события создания/обновления."
}
```

### 29.4. UI

- Форма создания/редактирования узла ([NodeSettings.tsx](../../web-ui/src/pages/NodeSettings.tsx)):
  отдельный блок-карточка «Комментарий» **в самом низу** левой колонки
  (`SectionHead` + `Textarea`, обычный шрифт, `rows=4`, `maxLength=2000`).
- Страница узла, вкладка «Обзор» ([OverviewTab.tsx](../../web-ui/src/components/node/OverviewTab.tsx)):
  read-only показ комментария, если он задан (`whitespace-pre-wrap`).
- i18n-ключи: `node.form.comment`, `node.form.comment_label`, `node.form.comment_hint`,
  `node.form.comment_placeholder` (ru/en).

### 29.5. Receiver

Receiver не читает и не использует `comment` — поле не участвует в маршрутизации.
В SELECT `nodecache` не добавляется; через write-through JSON-кеш Web поле может
долетать в Redis, но Receiver его игнорирует.

### 29.6. Out of scope (v1)

- История изменений комментария.
- Упоминания/уведомления.
- Отдельные права на редактирование (правит тот же manager+, что и форму узла).
