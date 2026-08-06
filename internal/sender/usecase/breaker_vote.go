package usecase

import (
	"context"
	"errors"
	"time"

	"nexus/internal/domain"
	"nexus/internal/sender/usecase/port"
)

// breakerVote — что исход вызова сообщает о здоровье ВНЕШНЕГО узла (§81.2).
type breakerVote int

const (
	voteHealthy   breakerVote = iota // узел ответил (статус < 500)
	voteUnhealthy                    // 5xx, наш таймаут, отказ транспорта
	voteAbstain                      // исход о здоровье узла не говорит ничего
)

// Пределы на записи, выполняемые ПОСЛЕ вызова на отвязанном контексте (§81.2).
// Небольшие: это best-effort записи в Redis, задерживать освобождение горутины
// ради них нельзя.
const (
	breakerBookkeepingTimeout = 2 * time.Second
	nodeStatusWriteTimeout    = 2 * time.Second
)

// classifyUpstream различает «приёмник не смог» и «ответа никто уже не ждёт».
//
// Воздержание — когда умер РОДИТЕЛЬСКИЙ контекст: ушёл клиент шины, останав-
// ливается сервис либо порвался транспорт Receiver→Sender (см. godoc
// domain.ReasonClientCanceled — исходов больше одного, «виноват клиент» из этой
// ветки не следует). Приёмник в этот момент может быть совершенно здоров — мы
// просто не дождались ответа. Засчитывать это отказом нельзя: боевой инцидент
// 06.08.2026 (§81.1) — приёмник отвечал 200 за 145 с, клиента обрывали на 50-й
// секунде, пять таких обрывов открывали breaker, а половинчато-открытая проба
// обрывалась ровно так же, и выхода из состояния не существовало.
//
// Дискриминатор — состояние РОДИТЕЛЬСКОГО контекста, а не текст ошибки:
// httpclient оборачивает вызов в собственный дедлайн узла, поэтому НАШ таймаут
// оставляет родителя живым (ctx.Err() == nil), а всё остальное его убивает — и
// неважно, Canceled это или чужой DeadlineExceeded. Разбор строк ошибок не нужен
// (CLAUDE.md §5). Плата за такую грубость — неразличимость исходов внутри ветки
// воздержания; она осознанная, см. godoc domain.ReasonClientCanceled.
//
// Порядок веток обязателен: живость родителя проверяется РАНЬШЕ признака
// истёкшего дедлайна, иначе чужой дедлайн, появившись выше по стеку, снова
// будет засчитан приёмнику.
func classifyUpstream(ctx context.Context, resp *port.HTTPResponse, lastErr error) breakerVote {
	switch {
	case lastErr == nil && resp != nil && resp.StatusCode < 500:
		// ЛЮБОЙ ответ < 500 — узел жив и отвечает: 4xx это ошибка данных или
		// клиента (напр. 422 NotRegistered протухшего FCM-токена), по ней breaker
		// НЕ открывается — иначе серия 4xx от «плохих» адресатов блокировала бы
		// доставку валидных запросов 503-ми (боевой инцидент site/push, §50.4).
		// Oversize-политика (§43-rev) на здоровье тоже не влияет: статус берётся
		// фактический, а не синтезированный 502.
		return voteHealthy
	case lastErr == nil:
		return voteUnhealthy // 5xx
	case ctx.Err() != nil:
		return voteAbstain // родительский контекст умер: ответа уже не ждут
	case errors.Is(lastErr, context.DeadlineExceeded):
		// Наш per-node timeout_ms: узел не уложился в собственный срок — это счёт
		// к нему. Настраиваемый вклад таймаута в счётчик — §80.5 п. 2, вне §81.
		return voteUnhealthy
	default:
		return voteUnhealthy // DNS, connection refused, TLS — адрес мёртв
	}
}

// voteBreaker учитывает исход вызова в circuit breaker'е узла (§81.2).
//
// Контекст здесь ОТВЯЗЫВАЕТСЯ от родительского намеренно. Учёт выполняется
// после вызова, и на обрыве клиента родитель уже мёртв, а go-redis отбрасывает
// команду с отменённым контекстом ещё в пуле соединений (`internal/pool.waitTurn`
// проверяет ctx.Done() до обращения к сокету) — то есть запись молча не
// выполнялась бы, а ошибка гасилась в «_». WithoutCancel, а не Background:
// сохраняем trace/log-значения запроса (CLAUDE.md §6).
func (u *SendUsecase) voteBreaker(ctx context.Context, in SendInput, resp *port.HTTPResponse, lastErr error, rec *domain.LogRecord) {
	if in.DryRun {
		// §55: тестовый вызов на здоровье узла не влияет — иначе серия dry-run по
		// мёртвому адресу открыла бы breaker и живой узел начал бы отдавать 503.
		u.logger.Debug("send: dry-run, breaker not touched",
			u.logger.Str("id", in.ID),
			u.logger.Int("status", int(rec.Status)))
		return
	}

	vote := classifyUpstream(ctx, resp, lastErr)
	if vote == voteAbstain {
		// Ни RecordFailure, ни RecordSuccess: поток обрывов не должен ни открывать
		// breaker живого приёмника, ни удерживать закрытым breaker мёртвого.
		u.logger.Debug("send: parent context died, breaker vote abstained",
			u.logger.Str("id", in.ID),
			u.logger.Str("node", in.NodePath),
			u.logger.Int("duration_ms", int(rec.Duration)),
			u.logger.Err(ctx.Err()))
		return
	}

	bookCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), breakerBookkeepingTimeout)
	defer cancel()

	if vote == voteHealthy {
		if err := u.cb.RecordSuccess(bookCtx, in.NodePath); err != nil {
			// §51.9: сбой учёта раньше гасился в «_» и был невидим полностью.
			u.logger.Debug("send: breaker success not recorded",
				u.logger.Str("id", in.ID),
				u.logger.Str("node", in.NodePath),
				u.logger.Err(err))
		}
		return
	}

	policy := domain.BreakerPolicy{
		Threshold: int(in.BreakerThreshold),
		Cooldown:  time.Duration(in.BreakerCooldownSec) * time.Second,
	}
	if err := u.cb.RecordFailure(bookCtx, in.NodePath, policy); err != nil {
		u.logger.Debug("send: breaker failure not recorded",
			u.logger.Str("id", in.ID),
			u.logger.Str("node", in.NodePath),
			u.logger.Err(err))
	}
	// §51.9: незасчитанное здоровье узла (открытие breaker'а после серии) —
	// след решения на debug.
	u.logger.Debug("send: recorded upstream failure for breaker",
		u.logger.Str("id", in.ID),
		u.logger.Str("node", in.NodePath),
		u.logger.Int("status", int(rec.Status)),
		u.logger.Str("reason", rec.Reason))
}

// failureReason — причина для лога и ответа клиенту, когда вызов не состоялся.
//
// Нормализуются два исхода (§81.2.1): смерть родительского контекста и
// собственный таймаут узла. Остальное возвращается текстом ошибки КАК ЕСТЬ.
//
// Важно понимать границу: этот текст — *url.Error от net/http, и он содержит
// адрес запроса («Post "https://host/path?query": dial tcp …»). У узлов с
// динамическим URL хвост собран из входящего запроса, то есть кусок входа всё
// ещё попадает в журнал. §81 нормализует только два самых частых исхода;
// сплошная санитизация текстов stdlib — отдельная работа, и до неё считать
// сырой текст безопасным нельзя.
//
// waitedMs — сколько фактически ждали; для отменённого вызова это единственная
// величина, которая что-то значит (сам таймаут узла к обрыву отношения не имеет).
func failureReason(ctx context.Context, err error, timeoutMs, waitedMs int32) string {
	switch {
	case ctx.Err() != nil:
		return domain.ReasonClientCanceled + ": waited " + itoa32(waitedMs) + " ms, no response"
	case errors.Is(err, context.DeadlineExceeded):
		return domain.ReasonUpstreamTimeout + ": " + itoa32(timeoutMs) + " ms"
	default:
		return err.Error()
	}
}

// itoa32 — форматирование без fmt: функция зовётся на каждую попытку.
//
// Через int64: у int32 отрицание MinInt32 остаётся отрицательным (переполнение),
// и наивный вариант вернул бы "-" вместо числа.
func itoa32(v int32) string {
	if v == 0 {
		return "0"
	}
	n := int64(v)
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
