# 51. Управление логированием сервисов (консоль «Логи»)

Раздел вводит наблюдаемость **служебных логов** трёх процессов шины (Receiver, Sender, Web) прямо из
SPA: консольный вьювер последних записей, runtime-смена уровня логирования без рестарта и скачивание
в файл. НЕ путать с логами запросов узлов (ClickHouse, §7.4/§42) — здесь речь о slog-выводе самих
сервисов (то, что сейчас видно только на сервере/в journald). Развивает §14 (логирование), §34.2
(hot-reload настроек), §30.2 (panic recovery — паники тоже станут видны в UI).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md). **Статус: ТЗ зафиксировано, реализация
не начата** (план — ветка `feature/service-logging`, блоки 51.1–51.6).

## 51.1 Зачем

- Расследования (§50: редиректы, breaker) упираются в то, что служебные логи Sender/Receiver видны
  только с доступом к серверу. Админ должен видеть их в UI.
- Уровень логирования сейчас фиксируется на старте (`logging.level` в YAML, int 2..5) — чтобы включить
  debug на бою, нужен рестарт всех сервисов. Нужна runtime-смена.
- Согласованные требования: логи **всех трёх сервисов**; обновление **по кнопке/автотаймеру** (не SSE);
  селектор уровня **меняет реальный порог** процессов; «SysLog»-тумблер — вне scope этой итерации.

## 51.2 Архитектурная развилка (решена)

Nexus — три процесса; логгер строится один раз в `bootstrap.Init` через вендорный
`logging.Initlogger` с **фиксированным** `slog.Level` (нет `LevelVar` — менять на лету нельзя), а
сборка цепочки хендлеров (tint/JSON + Sentry) происходит внутри вендора. Поэтому:

1. **Своя сборка цепочки хендлеров** в bootstrap из экспортируемых примитивов вендора
   (`NewMultiHandler`, `SentryHandler`, `NewLogger(*slog.Logger)`):
   `MultiHandler(stdout/file[level=*slog.LevelVar], ringHandler[тот же LevelVar], SentryHandler[sentry.level])`.
   `LevelVar` даёт runtime-смену уровня. Текущее поведение (tint в stderr / JSON в файл, Sentry-порог)
   сохраняется в точности.
2. **Кросс-процессная доставка логов в Web** — через общий Redis: каждый сервис пишет строки в
   `nexus:logs:<service>` (LPUSH + LTRIM до N + EXPIRE), Web читает три ключа и мержит. Иначе логи
   Sender/Receiver в UI не попадут.

## 51.3 Ядро: LevelVar + RingHandler + Redis-шиппер (блок 51.1)

- Новый пакет `internal/platform/logsink`: `RingHandler` (slog.Handler) — маскирует значения атрибутов
  по `sensitiveKeys` (зеркало `platform/sentry`), кладёт запись `{ts, level, svc, msg, attrs}` в
  in-process кольцевой буфер и в буферизованный канал; фоновая горутина (`safego.Recover`) батчами
  пишет в Redis (pipeline LPUSH+LTRIM+EXPIRE). **Канал полон → дроп** (лог-путь никогда не блокируется)
  + счётчик дропов.
- `bootstrap.Init` дополнительно возвращает `*LogController{level *slog.LevelVar; ring *RingHandler}`.
  Redis в момент `Init` ещё не создан → шиппер прикрепляется позже: `LogController.StartRedisShipper(ctx,
  redis, service)` из `app.New` каждого сервиса. In-process ring работает всегда (резерв при
  недоступном Redis).
- Начальный уровень — из `cfg.Logging.Level` (int 2=error..5=debug, как сейчас).
- Объём Redis: LTRIM ~2000 строк на сервис + EXPIRE ~1 ч (логи мёртвого сервиса истекают); ~2–3 МБ.

## 51.4 Runtime-уровень через app_settings + reload-шина (блок 51.2)

- Домен: `LoggingSettings{Level *int}` (секция `logging` в `app_settings`, JSONB — без миграции),
  валидация 2..5; `mergeAppSettings`/`changedSections` + publish `reloader.SectionLogging`.
- Применятель по образцу `SessionTTLProvider`/`applySessionTTL`: `applyLogLevel(ctx)` читает
  app_settings → `level.Set(...)`; nil → откат на `cfg.Logging.Level` из YAML.
- **Все три сервиса** подписываются на `SectionLogging` (сейчас Receiver — только Sentry, Sender —
  Sentry+CH); Receiver/Sender получают минимальный PG-ридер `app_settings.value→logging.level`
  (PG у них уже есть). Начальное значение — из app_settings-overlay в bootstrap.

## 51.5 API вьювера (блок 51.3, admin-only под `authedAdmin`)

- `GET /api/logs?service=all|receiver|sender|web&limit=N` → LRANGE нужных ключей, парс, мерж по
  времени (desc), срез до N. Ответ: массив `{ts, level, service, msg, attrs}`.
- `GET /api/logs/download?service=&limit=` → `text/plain; Content-Disposition: attachment`
  (зеркало audit CSV / body/download §42), имя `nexus-logs-<ts>.log`.
- Смена уровня — существующий `PUT /api/settings/app {logging:{level}}` (admin), без отдельного endpoint.

## 51.6 UI: консоль follow-tail отдельным пунктом сайдбара (блок 51.4)

Согласовано: **не модалка и не вкладка настроек** — отдельный пункт сайдбара «Логи» (admin, рядом с
Audit/Kafka), маршрут `/logs` с admin-guard (образец `AuditRoute`), полная высота. Раскладка —
консоль follow-tail (стиль Vercel/Railway):

- **Липкий тулбар:** чипсы сервисов `[Все · Receiver · Sender · Web]` (фильтр отображения);
  **сегментированный уровень** `⟨Error Warn Info Debug⟩` — onChange меняет РЕАЛЬНЫЙ уровень через
  `PUT /api/settings/app` (не только фильтр показа); поле поиска (клиентская подстрока); индикатор
  `● Live` (refetchInterval 2–3 с) с паузой; кнопка «↧ Скачать» (`<a href download>`).
- **Поток:** плотные моноширинные строки, новые снизу, автоскролл при Live, **пауза автоскролла при
  ручном скролле вверх** + кнопка «↓ к последним»; цветные бейджи уровня (warn/err токены) + тег
  сервиса; строка раскрывается в структурные атрибуты (`node=… status=… from=…`). Кап буфера ~2000
  строк (DOM не растёт).
- i18n `nav.logs` («Логи») + `logs.viewer.*` en/ru синхронно; пересборка embed-бандла обязательна.

## 51.7 Верификация (блок 51.6)

Гейты сдачи: полный `make test-integration`, полный `-race` в контейнере golang:1.26, браузерный
прогон на стенде: (1) консоль показывает строки всех трёх сервисов (редирект-узел из §50 даёт
`sender follows external redirect`); (2) смена Info→Warn глушит Info во ВСЕХ сервисах без рестарта и
переживает перезаход; (3) Live/пауза/«к последним»; (4) скачивание файла; (5) чувствительные значения
замаскированы `***`; (6) лог-путь под нагрузкой не блокируется (дроп, не задержка).

## 51.x Scope (что не входит)

- «SysLog»-тумблер (чтение journald или дубль в syslog) — сознательно отложен.
- SSE-стриминг служебных логов — не в этой итерации (обновление по кнопке/таймеру); при потребности —
  переиспользовать транспорт `/logs/stream` (§48.6).
- Персистентное хранилище служебных логов (CH/файлы с ротацией) — только кольцо в Redis (~2000
  строк/сервис, EXPIRE 1 ч); для долгой истории есть Sentry (ошибки) и вывод в файл (`output_in_file`).
- Per-service уровень (у каждого сервиса свой) — один общий уровень на все три; при потребности —
  расширение `LoggingSettings`.
