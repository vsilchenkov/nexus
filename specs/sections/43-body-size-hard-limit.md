## 43. Лимиты размера тела: per-node лог-усечение vs транспортные лимиты из конфига

> **История.** Первая редакция §43 ошибочно сделала per-node `max_body_size` жёстким reject (413/502).
> Это исправлено: `max_body_size` снова режет ТОЛЬКО лог (§22.2), а 413/502 даёт превышение
> **транспортных лимитов из конфига**.

### 43.1. Два разных механизма

| Что | Где задаётся | Единица | Что делает при превышении |
|---|---|---|---|
| `max_body_size` (тумблер «Ограничить размер тела») | per-node, форма узла (PostgreSQL) | руны | Режет ТОЛЬКО копию тела в ClickHouse-логе (`…(truncated)`). На ответ клиенту и HTTP-код НЕ влияет; checksum по полному телу (§22.2) |
| `receiver.max_body_bytes` | config | байты | Тело ЗАПРОСА больше → **413** (Receiver, до Sender; sync/async/callback) |
| `sender.grpc_max_message_bytes` (= `receiver.sender_grpc.max_message_bytes`) | config | байты | Тело ОТВЕТА апстрима больше → **502** (Sender, ответ не отдаётся клиенту) |

Связи лимитов (инварианты, продублированы комментарием в `config.example.yml`/`config.yml`):
`receiver.max_body_bytes` **<** `grpc_max_message_bytes` и **<** `kafka.topic.max_message_bytes` (запас под
envelope); `kafka.consumer.fetch_max_bytes` **≥** `topic.max_message_bytes`; брокерский `message.max.bytes`
**≥** topic.

### 43.2. Запрос больше `receiver.max_body_bytes` → 413

`readBody` ([receiver/.../handler.go](internal/receiver/adapter/in/http/handler.go)) читает тело с
`io.LimitReader(max+1)`; при превышении возвращает sentinel `errBodyTooLarge`, который `replyReadBodyError`
мапит в **413 Payload Too Large** (а не 400). Покрывает sync/async/callback. В ClickHouse такая запись не
пишется (Receiver лог не ведёт) — код клиенту + WARN.

### 43.3. Ответ больше `grpc_max_message_bytes` → 502 (memory-safe)

Тело ответа физически ограничено gRPC-транспортом Sender→Receiver. httpclient
([sender/.../httpclient/client.go](internal/sender/adapter/out/httpclient/client.go)) читает ответ через
`io.LimitReader(maxResp+1)`, где `maxResp = sender.grpc_max_message_bytes − запас под envelope`. Если тело
перевалило за лимит — `port.HTTPResponse{TooLarge: true}` без тела (НЕ затягиваем гигантский ответ в память
— защита от OOM). `SendUsecase.Send` ([send.go](internal/sender/usecase/send.go)): при `resp.TooLarge` →
клиенту **502**, лог `done=0`, `reason="response body exceeds transport limit: > N bytes"`, тело и checksum
не пишутся (тело не прочитано). Circuit breaker — по реальному статусу upstream.

### 43.4. `max_body_size` снова усекает только лог (§22.2)

Восстановлены `truncateRunes`/`truncationMarker`: `rec.Request`/`rec.Response` режутся по per-node
`max_body_size` (руны), `checksum_*` — по полному телу, клиент получает полный `resp.Body`. Поведение
полностью соответствует §22.2.

### 43.5. Тесты

- send_test: `TestTruncateRunes`; `max_body_size` → лог усечён + маркер, КЛИЕНТ получает полное тело,
  checksum по полному; `resp.TooLarge` → 502 (done=0, reason, без тела/checksum); under-limit → 200.
- httpclient: ответ > лимита → `TooLarge`, тело не дочитано; ≤ лимита → полное.
- receiver handler: тело запроса > `max_body_bytes` → 413; `readBody`/`replyReadBodyError` маппинг.
- integration: лимит off → полное; лимит on → лог усечён + клиент полное; ответ > конфиг-лимита → 502.
