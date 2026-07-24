# 68. Поддержка multipart/form-data

Раздел закрывает работу с запросами и ответами в формате **`multipart/form-data`** (и прочими
`multipart/*` — `mixed`, `related`): полная проброска тела с вложениями от отправителя к получателю
во всех видах отправки, честный учёт размера сообщения с вложениями и отказ от хранения самого
вложения в ClickHouse (вместо тела — компактный плейсхолдер со сводкой частей). Развивает §4.3
(схема логов), §22.2 (обрезка тел), §42.10 (размеры тел), §7.4.1 (replay), §43 (лимит тела).

Карта реализации — [../IMPLEMENTATION.md](../IMPLEMENTATION.md).

## 68.1 Постановка и решения

1. **Проброска всех данных отправитель → получатель во всех видах отправки.** Тело запроса шина
   и так везде переносит как непрозрачные байты, а `Content-Type` (вместе с `boundary`)
   пробрасывается дословно — sync (gRPC `bytes body`), async (JSON-конверт, тело в base64), pull
   RabbitMQAsync (§27), dry-run (§55). Отдельного кода поэтому не потребовалось; поведение
   зафиксировано матрицей §68.4 и тестами.
2. **Размер сообщения — с учётом вложения.** Истинные размеры `request_size`/`response_size`
   (§42.10) уже считаются по **полному** телу в байтах до усечения лог-копии — вложения в них
   учтены. Менять нечего.
3. **В ClickHouse не хранить тело вложения.** В колонки `request`/`response` вместо multipart-тела
   пишется §68-плейсхолдер (см. §68.2). Само вложение в CH не попадает.

**Решения (утверждены заказчиком):**

- **Multipart освобождён от пер-узлового лимита тела `max_body_size` (§22.2/§43).** Тело в CH и так
  не хранится, а плейсхолдер короткий (ограничен константами), поэтому `truncateRunes` к нему не
  применяется. Действуют только транспортные капы: `receiver.max_body_bytes` (5 МиБ, дефолт → 413)
  и лимит Kafka-сообщения для async (~10 МиБ, с учётом base64 +33% на конверт).
- **Плейсхолдер — со сводкой частей** (имя поля, `filename`, тип, размер), без содержимого.
- **Правило симметрично для ответов** внешней системы с `multipart/*` Content-Type (например
  `multipart/mixed` при скачивании) — в колонку `response` тоже плейсхолдер.

## 68.2 Плейсхолдер вместо тела

Домен-хелперы — [domain/multipart.go](../../internal/domain/multipart.go):

- `IsMultipartMediaType(ct)` — `Content-Type` относится к `multipart/*` (через `mime.ParseMediaType`,
  регистр и параметры игнорируются).
- `MultipartLogPlaceholder(ct, body)` — строит текст плейсхолдера. Формат:

  ```
  multipart/form-data
  - part 1: name="file"; filename="invoice.pdf"; type=application/pdf; size=1048576
  - part 2: name="comment"; type=text/plain; size=27
  [2 parts, body 1048603 bytes]
  ```

  Первая строка — media type (по ТЗ). Далее по строке на часть; размер части считается потоково
  (`io.Copy(io.Discard, part)`), содержимое частей/значения полей в вывод **не попадают**. Потолки:
  `multipartMaxListedParts = 50` (перечисляется не больше, полное число — в итоговой строке),
  `multipartScanCap = 1000`. Неразобранное тело (нет `boundary`, оборвано) → безопасный fallback
  `multipart/form-data\n[multipart body not stored; <причина>; body N bytes]`. Первой строкой
  всегда валидный multipart media type — на него опирается детект. Размер части — после декодирования
  `Content-Transfer-Encoding` (mime/multipart декодирует quoted-printable/base64 прозрачно); для
  сводки это приемлемо.
- `IsMultipartLogPlaceholder(stored)` — структурный детект: 1-я строка — multipart media type,
  2-я — строка-часть (`- part …`) или сводка (`[`). Настоящее multipart-тело начинается с
  `--<boundary>`, JSON — с `{`/`[` в первой строке, поэтому под критерий не подпадают.

**Точка подстановки — Sender.** [sender/usecase/send.go](../../internal/sender/usecase/send.go),
`SendUsecase.Send`: приватный `logBodyCopy(kind, contentType, body, in)` для multipart отдаёт
плейсхолдер (без `truncateRunes`, с debug-следом §51.9), иначе — усечённую по `max_body_size`
копию как раньше. Подключён к запросу (по `in.Headers`) и к ответу (по `resp.Headers`). `checksum`,
размеры, тело к внешнему узлу и ответ клиенту **не меняются** — считаются по полному телу. Гейты
`LogRequestBody`/`LogResponseBody`/`LoggingEnabled`/`DryRun` сохранены. Kafka-retry логов (§38)
получает плейсхолдер автоматически: он «запечён» в `LogRecord` до записи. **Схема CH и миграции не
трогаются** — колонки те же.

## 68.3 Replay multipart-записи

У multipart-записи в `request` хранится плейсхолдер, а не тело — отправить его во внешний target
нельзя. [web/usecase/replay.go](../../internal/web/usecase/replay.go), `replayOne`: при
nil-`BodyOverride` и `IsMultipartLogPlaceholder(orig.Request)` возвращается сентинел
`ErrReplayBodyMultipart` → handler отдаёт **422** (`replay.multipart_unavailable`, en+ru). Ручной
`BodyOverride` разрешён — оператор вводит тело сам. Массовый `ReplayFailed` (без override) кладёт
такие записи в `Failed` — без двойной доставки и мусора в target.

**UI** ([web-ui/src/components/ReplayDialog.tsx](../../web-ui/src/components/ReplayDialog.tsx)):
TS-зеркало детекта определяет плейсхолдер в уже загруженной детали лога → warn-подсказка
(`replay.multipart_warning`) под полем тела и дизейбл кнопки, пока тело не введено вручную. Новых
запросов нет.

**Известное ограничение.** Multipart-записи, сделанные **до** §68 (сырое тело в CH), детект не
ловит — replay отправит сохранённое как раньше. Это осознанный компромисс (альтернатива —
флаг-колонка в CH — отвергнута ради «без миграций»).

## 68.4 Проброска: матрица (что проверено, кода не потребовалось)

| Путь | Где | Вердикт |
|---|---|---|
| Sync gRPC | [receiver/usecase/route.go](../../internal/receiver/usecase/route.go), `proto/sender/v1/sender.proto` | `Content-Type` c `boundary` дословно, тело `bytes` байт-в-байт |
| Async Kafka | [receiver/usecase/envelope.go](../../internal/receiver/usecase/envelope.go) | Тело `[]byte` → base64 в JSON-конверте; переживает transit. Инвариант `max_body_bytes < kafka.topic.max_message_bytes` (§43.1) |
| RabbitMQAsync (§27) | [receiver/usecase/puller.go](../../internal/receiver/usecase/puller.go) | `msg.ContentType` → `Content-Type` (дефолт `application/octet-stream`), тот же async-путь |
| Dry-run (§55) | [web/usecase/dry_run.go](../../internal/web/usecase/dry_run.go) | Тело/заголовки оператора as-is; `LoggingEnabled=false` — CH не пишется, плейсхолдер не нужен |
| Клиент/ответ | send.go | Клиент получает полное тело/ответ; плейсхолдер только в лог-копии |

## 68.5 Тестирование и стенд-проверка

- Unit: `domain/multipart_test.go` (table-driven + `FuzzMultipartLogPlaceholder`), `send_test.go`
  (плейсхолдер вместо тела + полный размер/checksum + проброска байт-в-байт; освобождение от
  `max_body_size`; `LogRequestBody=false` → пусто; lowercase заголовок; multipart-ответ; регресс
  не-multipart), `replay_test.go` (422 без override / прохождение с override).
- Стенд ([docs/STAND_TESTING.md](../../docs/STAND_TESTING.md)): `curl -F "file=@big.bin" -F meta=…`
  в `/v1/request/…` и `/v1/requestAsync/…`; на target-заглушке файл цел побайтово; в UI «Логи» —
  плейсхолдер + честный размер; replay даёт предупреждение/422; файл > 5 МиБ → 413.
