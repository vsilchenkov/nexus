-- 0043_node_sse_breaker_timeout.up.sql
--
-- §107 ТЗ: две настройки узла для потоковых подписок и медленных ответов.
--
-- sse_decline — «Не открывать поток (SSE) на GET». Receiver сам отвечает 405
-- на GET с Accept: text/event-stream, не вызывая получателя: шина не умеет
-- передавать бесконечный поток (Sender дочитывает тело до конца), и подписка
-- висела до таймаута узла. Имеет смысл только у sync-узла (root_method =
-- request); у остальных нормализация домена сбрасывает флаг.
--
-- breaker_ignore_timeouts — «Не считать таймаут отказом защиты». Таймаут при
-- установленном соединении (получатель принял запрос, но не ответил вовремя)
-- не засчитывается circuit breaker'у узла. Отказ соединения, DNS, TLS и
-- таймаут соединения по-прежнему считаются. Закрывает §80.5 п. 2.
--
-- NOT NULL DEFAULT false атомарно заполняет все существующие узлы прежним
-- поведением — отдельный backfill не нужен. Миграция аддитивная (§74.2):
-- старый код колонок не читает и работает как раньше.

ALTER TABLE nodes
    ADD COLUMN sse_decline BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE nodes
    ADD COLUMN breaker_ignore_timeouts BOOLEAN NOT NULL DEFAULT false;
