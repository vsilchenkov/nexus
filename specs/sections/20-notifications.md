## 20. Уведомления операторам (Telegram)

Реализовано в Phase F2 (фазы F2.1–F2.6). Карта реализации с привязкой к коду —
в [../IMPLEMENTATION.md](../IMPLEMENTATION.md). Ранее значилось в
[16-out-of-scope.md](16-out-of-scope.md) как план на v2.

Назначение: Web Service по cron-расписанию проверяет ошибки узлов за период и
шлёт сводку в Telegram-чат — **только если ошибки есть**. Настраивается админом,
поддерживает тестовую отправку.

### 20.1 Модель настроек

- Блок в `domain.AppSettings`: `notifications.telegram` =
  `{enabled, chat_id, bot_token, cron}` — все поля-указатели (nil = не задано),
  как Sentry/ClickHouse. Хранится в singleton `app_settings` (JSONB).
- Конфиг-секции в `config.yml` нет — Telegram нужен только Web и только в
  рантайме; env-дефолтов для секретов нет (операторские секреты — через UI).
- `bot_token` маскируется в `Get()` (`***`), не перезаписывается при merge
  значением `***`. `cron` валидируется (`cron.ParseStandard`) при сохранении и
  тесте; невалидное выражение → ошибка.
- Изменение секции публикует reload-событие `notifications` (Redis pub/sub),
  планировщик пересоздаёт расписание без рестарта.

### 20.2 Планировщик

- `NotificationScheduler` (Web Service): `Run(ctx)` + `Reschedule(ctx)`. Тики
  дёргает `robfig/cron/v3` (стандартный 5-полевой cron).
- Hot-reload: при смене выражения cron пересоздаётся целиком (robfig не меняет
  spec у entry); при `enabled=false` — останавливается. Под mutex.
- Источник ошибок — логи ClickHouse за окно `(last_check, now]`: по каждому узлу
  с непустым `clickhouse_table` считается число записей со
  `status>=400 OR status=0 OR done=0` (`LogReader.CountErrors`). Узлы без
  таблицы и отсутствующие таблицы пропускаются.
- Окно хранится в Redis (`nexus:notif:last_check`, unix-ms) — переживает
  рестарты и общий между репликами. После успешной отправки (или при отсутствии
  ошибок) checkpoint двигается на `now`; при ошибке отправки — НЕ двигается
  (ошибки попадут в следующий тик).

### 20.3 Multi-instance

При нескольких репликах Web cron сработает в каждой. Распределённый лок Redis
`SET nexus:notif:lock <v> NX PX <ttl>` (TTL ~3 мин) гарантирует ровно одну
отправку на тик; реплика без лока молча выходит. Лок не освобождается вручную —
полагаемся на TTL.

### 20.4 Telegram-клиент и формат

- `platform/telegram.Client.Send(token, chatID, text)` → POST
  `https://api.telegram.org/bot<token>/sendMessage` (`parse_mode=HTML`,
  `disable_web_page_preview`). Свой `http.Client` (timeout 10s).
- Сообщение: заголовок + окно (UTC) + `total errors: N`; по командам
  `<name> (<slug>): T`, под ней `• <path> [<table>]: <count>`. HTML-экранирование
  `<>&`. Резка по 4096 (порог 4000) → несколько сообщений.

### 20.5 API / UI

- `GET/PUT /api/settings/app` — секция `notifications` (как Sentry/ClickHouse).
- `POST /api/settings/notifications/test` — admin-only; тестовая отправка с
  merge'нутыми настройками (маскированный токен не используется), 503 при
  недоступном tester'е.
- UI: Settings → Notifications (admin-only): `enabled`, `chat_id`, `bot_token`
  (password + маска), `cron` + подсказка; кнопки Save и «Проверить».

### 20.6 Scope / неочевидности

- Один глобальный бот на систему (singleton `app_settings`); сводка
  агрегирует ошибки всех команд. Per-team-конфигурация — возможное развитие.
- Планировщик живёт в Web и требует ClickHouse (через `LogReader.CountErrors`):
  без CH уведомления не работают.
- **Критичная неочевидность:** `AppSettingsRepoPg.Update` обязан сериализовать
  секцию `notifications` в JSONB — иначе настройки молча теряются (см. round-trip
  тест в integration).
