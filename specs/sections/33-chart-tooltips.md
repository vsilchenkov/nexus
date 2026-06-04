## 33. Доработка тултипов графиков (chart tooltips)

Все графики Nexus (спарклайн в карточке узла, бар-чарт трафика узла, throughput/lag-графики Kafka,
KPI-спарклайны) при наведении показывают **бедный тултип**: спарклайн узла — `15:43–16:13 · 724`
одной строкой без иерархии; Kafka-графики — дефолтный тултип recharts с плоским списком; спарклайн
Overview — нативный HTML-атрибут `title`. Нет единого визуального языка, разбивки по сериям с цветными
маркерами, единицы измерения, дельты к соседнему периоду и опционального действия.

Раздел вводит **единый компонент тултипа** `<ChartTooltip>` с устойчивой иерархией информации и
переиспользует его во всех графиках. Реализация — **строго под текущую архитектуру фронтенда**:
recharts 3.8.1 + Radix Tooltip (`@radix-ui/react-tooltip`), **без новых зависимостей** (в частности
без `@floating-ui/react`, который предлагал эталонный макет), на **уже доступных данных API**.

Эталон дизайна — [../nexus_chart_tooltip.html](../nexus_chart_tooltip.html) (5 сценариев: спарклайн
«до/после», multi-line, lag c партициями, сравнение с прошлым периодом, маркер ошибки). Часть
сценариев макета (разбивка lag по партициям, сравнение «неделю назад») требует данных, которых
backend сейчас не отдаёт, — они вынесены в out-of-scope (§33.7).

### 33.1. Цель и принципы

Единый визуальный язык тултипов для всех графиков с устойчивой **иерархией информации сверху вниз**:

1. **Период** — контекст времени: точка или интервал (`15:43 → 16:13`) + длительность отдельным
   мелким шрифтом-бейджем (`30 мин`), либо короткая заметка о шаге (`30-секундное окно`).
2. **Главное значение** — крупно (mono, ~24px, bold). То, что глаз ловит первым. Цвет зависит от
   категории: обычное — `--fg`, предупреждение — `--warn` (оранжевое), ошибка — `--err` (красное).
   **Зелёный (`--ok`) для главного значения не используется** — он зарезервирован под дельты.
3. **Серии** — детализация: цветной маркер слева, имя серии, значение справа. **Сортировка по
   убыванию значения** (доминирующая серия — первой).
4. **Подвал** — один короткий вывод или дельта: «пик за период», «consumed < produced ∆ −294/с»,
   «всплеск ошибок». **Один признак, не два** — иначе теряется фокус.
5. **Действие** (опционально) — строка-ссылка: «Открыть логи за этот момент».

**Главное правило маркеров:** маркер серии в тултипе точно повторяет стиль этой серии на самом
графике — квадрат 8×8 для bar-серий, тонкая линия 10×2 для line-серий, пунктир для dashed-серий.
Иначе пользователь не сопоставит строку тултипа с линией на графике.

Шрифты: подписи серий — `--font-sans`; все числа и время — `--font-mono` (числа выравниваются по
разрядам, читать легче).

### 33.2. Единый компонент `<ChartTooltip>`

Новый презентационный компонент `web-ui/src/components/ui/ChartTooltip.tsx` — чистая отрисовка по
пропсам, без знания об источнике данных (его наполняют мапперы каждого графика). Пропсы:

```ts
type ChartTooltipProps = {
  period: {
    from: string;            // "15:43" или "пн, 31 мая · 13:30"
    to?: string;             // "16:13" — если задано, рисуется "from → to"
    durationLabel?: string;  // бейдж справа: "30 мин"
    note?: string;           // вместо длительности: "30-секундное окно" / "момент времени"
  };
  primary?: {
    value: string;           // уже отформатировано (fmtNum): "724", "1,180"
    unit?: string;           // "запросов", "RPS", "сообщений"
    tone?: "normal" | "warn" | "err";
  };
  series?: Array<{
    marker: "bar" | "line" | "dashed";
    color: string;           // токен-производный hex/var серии графика
    name: string;
    value: string;           // отформатировано
    unit?: string;           // "/с"
    muted?: boolean;         // приглушить (например, ряд сравнения)
  }>;
  footer?: {
    tone?: "muted" | "warn" | "err";
    icon?: ReactNode;        // иконка (lucide/tabler), опционально
    text: string;
    delta?: { dir: "up" | "down"; text: string }; // "+24% к среднему" / "−294/с"
  };
  action?: { icon?: ReactNode; label: string; onClick: () => void };
  compact?: boolean;         // ужатый режим для спарклайнов (period + primary)
};
```

Визуал — по эталону макета, **через токены Tailwind и `cn()`, без хардкода hex** в компоненте:

- Контейнер: полупрозрачный фон `bg-bg/95` + `backdrop-blur`, граница `border-line-strong`
  (0.5px-эффект), двойная тень (`shadow-lg` + тонкая внутренняя), скругление `rounded-md`,
  паддинг `px-3 py-2.5`, `min-w` ~140px (compact) / ~200px (полный).
- Период: flex-строка, mono, `text-fg-subtle`; разделитель `→`/`·`; бейдж длительности —
  `bg-bg-muted text-fg-muted` pill.
- Главное значение: mono, ~24px, bold, цвет по `tone` (`text-fg` / `text-warn` / `text-err`); юнит —
  мельче, `text-fg-muted`.
- Строка серии: маркер (`bar` — `h-2 w-2 rounded-sm`, `line` — `h-0.5 w-2.5 rounded`, `dashed` —
  `border-t-2 border-dashed`), имя `text-fg-muted`, значение mono `text-fg` (приглушённое при `muted`).
- Подвал: верхняя граница `border-line`, цвет по `tone`; дельта справа — `text-ok` (up) / `text-err`
  (down).
- Действие: верхняя граница `border-line`, `text-accent`, hover → `text-fg`, `cursor-pointer`.

Соответствие классов эталона (для реализации в Tailwind): `.tip`, `.tip-period`, `.tip-big`,
`.tip-row`, `.tip-foot`, `.tip-action`, `.partitions` (см. [../nexus_chart_tooltip.html](../nexus_chart_tooltip.html)).
Палитра-маппинг макет → токены проекта: `--text-info` = `accent` (#5b9bf0), `--text-success` = `ok`
(#4cc38a), `--text-warning` = `warn` (#d9a441), `--text-danger` = `err` (#e85d5c), `--border-strong`
= `line-strong`. Цвета серий графиков (`COLOR` в `charts.tsx`) переиспользуются как `color` маркеров.

### 33.3. Интеграция по типам графиков

**ThroughputChart (Kafka, recharts)** — [web-ui/src/components/kafka/charts.tsx](../../web-ui/src/components/kafka/charts.tsx).
Заменить дефолтный `<Tooltip contentStyle=… formatter=… />` на `<Tooltip content={<ChartTooltipRechartsAdapter … />} />`.
Адаптер мапит `payload` recharts → пропсы: `period.from` = `fmtTime(label)`, `period.note` = шаг
(`step_seconds`), серии produced/consumed/errors (маркеры `line`/`line`/`dashed`, цвета `COLOR`),
сортировка по убыванию. Подвал — при `consumed < produced` показать предупреждение
«consumed < produced» + дельта `−(produced−consumed)/с` (`tone: "warn"`). Snap к точке и
позиционирование — штатные у recharts (`content`-проп получает активную точку).

**LagChart (Kafka, recharts)** — там же. Аналогично через `content`-адаптер: `primary` = lag-агрегат
(`fmtNum`), `tone` = `warn`/`err` при приближении/превышении `threshold`; подвал — предупреждение о
пороге («критичный порог: N»). Partition-breakdown из макета **не реализуется** — данных нет (см.
§33.7); оставить в коде заметку-задел `// TODO §33.7: per-partition lag — нет данных в timeseries`.

**TrafficChart (узел, кастомный SVG + Radix)** — [web-ui/src/components/ui/TrafficChart.tsx](../../web-ui/src/components/ui/TrafficChart.tsx).
Заменить inline-JSX в `content=` существующего Radix `Tooltip` на `<ChartTooltip>`: `period` = окно
бакета (`from→to`, длительность), `primary` = `count` запросов, серии «доставлено»/«ошибок» (маркеры
`bar`, цвета `accent`/`err`), подвал — % ошибок и/или дельта к соседнему столбцу (вычисляется на
фронте из массива `data`: `(d.count - prev.count)`). `action` — переход в логи (см. §33.4).

**MiniSpark (KPI Kafka, recharts)** — там же. Сейчас тултипа нет. Добавить лёгкий тултип: т.к.
`MiniSpark` рисует голый `AreaChart`, обернуть в recharts `<Tooltip content=…>` с `compact`-режимом
`ChartTooltip` (period + primary). Анимация выключена (`isAnimationActive={false}`) — сохранить.

**Sparkline (карточка Overview, нативный `title`)** — [web-ui/src/pages/Overview.tsx](../../web-ui/src/pages/Overview.tsx).
Заменить атрибут `title` на Radix `Tooltip` + `ChartTooltip` в `compact`-режиме (period-бакет +
primary «N запросов»). Компромисс производительности (на Overview много карточек × много столбцов):
`TooltipProvider` уже общий (в `AppShell`), Radix монтирует контент **лениво на hover** — приемлемо.
Использовать `delayDuration` ~150 ms (§33.6).

### 33.4. Действие «Открыть логи за момент»

Для графиков **в контексте конкретного узла** (TrafficChart на вкладках узла) тултип может нести
`action`, навигирующий на логи узла за интервал бакета. Backend-поддержка уже есть:
`GET /api/nodes/{id}/logs?from=&to=` (фильтр по `date_request`, RFC3339/UnixMilli). Навигация —
react-router на страницу узла, вкладка `logs`, с query-параметрами `from`/`to` = границы бакета
(опц. `status=err` при наличии ошибок в точке). Реализация — только проброс `from/to`; формат уже
поддержан фронтом и backend'ом. Для Kafka-графиков (нет привязки к одному узлу) `action` не задаётся.

### 33.5. i18n

Новые ключи добавляются синхронно в оба бандла [../../web-ui/src/locales/en.json](../../web-ui/src/locales/en.json)
и [../../web-ui/src/locales/ru.json](../../web-ui/src/locales/ru.json) (паритет ключей — требование
CI). Это **клиентские строки** тултипов — backend-parity (`platform/i18n`) не требуется (тултипы не
приходят с сервера). Предлагаемый namespace и набор:

- `metrics.tooltip.delivered` — «доставлено» / "delivered"
- `metrics.tooltip.errors` — «ошибок» / "errors"
- `metrics.tooltip.requests_unit` — «запросов» / "requests"
- `metrics.tooltip.peak` — «пик за период» / "peak in range"
- `metrics.tooltip.vs_avg` — «{pct}% к среднему» / "{pct}% vs avg"
- `metrics.tooltip.open_logs` — «Открыть логи за этот момент» / "Open logs for this moment"
- `kafka.tooltip.produced` / `kafka.tooltip.consumed` / `kafka.tooltip.errors` — подписи серий
- `kafka.tooltip.rate_unit` — «/с» / "/s"
- `kafka.tooltip.consumed_lt_produced` — «consumed < produced» (как есть, технический)
- `kafka.tooltip.lag_unit` — «сообщений» / "messages"
- `kafka.tooltip.threshold_warn` — «критичный порог: {n}» / "critical threshold: {n}"
- длительности (`30 мин`, `1 мин`, `30-секундное окно`) — формируются из существующих хелперов
  периода (`web-ui/src/lib/period.ts` / `format.ts`), отдельные ключи только для слов «мин»/«сек»/
  «момент времени» при отсутствии.

### 33.6. Поведение и UX

- **Задержка появления** ~150 ms, **исчезновение мгновенно** — убирает мерцание при быстром проходе
  курсора по столбцам (для Radix — `delayDuration={150}`; для recharts-тултипа — поведение штатное).
- **Snap к ближайшей точке данных** — тултип не дрожит за курсором посимвольно (recharts привязывает
  `content` к активной точке; для bar-/spark-вариантов привязка к столбцу).
- **Auto-flip у краёв** — из коробки: recharts удерживает тултип в области графика; Radix —
  `side`/collision avoidance. Стрелка-указатель — у Radix (`Arrow`), для recharts опционально.
- **Деградация:** при `prometheus_available: false` / пустых данных тултип не показывает мусор —
  графики и так рисуют заглушку «нет данных» (`metrics.no_data`), тултип просто не появляется.

### 33.7. Scope / Out of scope

**В scope:** компонент `<ChartTooltip>`; интеграция во все пять мест (ThroughputChart, LagChart,
TrafficChart, MiniSpark, Sparkline Overview); приведение цветов к токенам; новые i18n-ключи;
действие «открыть логи за момент» для графиков узла.

**Out of scope (задел на v2 — требует backend-доработок):**

- **Разбивка lag по партициям Kafka** (сценарий 3 макета, мини-bar-chart по `p0..pN`). Prometheus
  отдаёт lag **агрегатом** (`sum(nexus_kafka_lag)`); per-partition lag в timeseries отсутствует
  (есть только aggregate consumer-group lag через Kafka Admin в `/api/kafka/topics`). Для реализации
  нужен новый источник per-partition временного ряда.
- **Сравнение «неделю назад» / «вчера»** для рядов (сценарий 4 макета, пунктирная линия и строка
  сравнения). Backend считает дельту только к **предыдущему периоду той же длины** для Kafka-overview
  (`kafkaDeltaDTO`); ряда-сравнения за прошлую неделю в timeseries нет. Дельта к предыдущему периоду
  может использоваться в подвале там, где она уже доступна, но отдельный ряд сравнения — v2.
- Новая зависимость `@floating-ui/react` (макет рекомендовал) — **не вводится**; позиционирование на
  recharts/Radix.
