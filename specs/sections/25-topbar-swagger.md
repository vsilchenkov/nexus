## 25. Swagger в глобальной шапке (Topbar utility zone)

Раздел добавляет иконку **документации API** в утилитарную зону глобальной шапки и вводит
генерацию/раздачу **двух** swagger-доков. До этого генерировался и раздавался только Web API по
`/swagger/index.html`; Receiver-док не существовал. Мокап — [../ui_topbar_utility.html](../ui_topbar_utility.html).

### 25.1. Два swagger-дока

`make swagger` генерирует оба дока (см. §11):

- **Web API** → `docs/web` (instance `swagger`, default) — контракт админки `/api/*`.
- **Receiver API** → `docs/receiver` (instance `receiver`) — публичный контракт `/v1/*`
  (аннотации в `cmd/receiver/main.go` и `internal/receiver/adapter/in/http/handler.go`).

Изоляция генерации через `--exclude`, чтобы web-док не подхватил `/v1`-маршруты, а receiver-док —
web-handler'ы (cross-package типы). Оба встраиваются в **Web-бинарь** через `embed.FS` (Receiver —
отдельный процесс без swagger UI; мокап это и предписывает).

### 25.2. Раздача

Web (`internal/web/app.go`) раздаёт оба дока двумя `ginswagger.WrapHandler(..., InstanceName(...))`:

- `GET /swagger/web/*any` — Web API (instance `swagger`);
- `GET /swagger/receiver/*any` — Receiver API (instance `receiver`).

Старый `/swagger/index.html` редиректится на `/swagger/web/index.html` (обратная совместимость).
CI-гейт `swagger-drift-check` проверяет оба `docs/`.

### 25.3. UI

`components/Topbar.tsx` (`SwaggerMenu`): иконка `FileText` с индикатором `↗` (внешний переход) и
tooltip; клик открывает popover из двух пунктов (Receiver `/v1/*`, Web `/api/*`), каждый —
`window.open(url, '_blank')`. Реализовано на Radix Popover/Tooltip (§17), `TooltipProvider`
монтируется один раз в `AppShell`. Если в инсталляции останется один док — popover можно свернуть в
прямую ссылку. Существующие переключатели языка/темы/выхода не меняются.
