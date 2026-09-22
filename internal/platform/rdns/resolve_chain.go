package rdns

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Значения атрибута source в Debug-строке исхода резолва (§51.9, §106.2).
const (
	sourceRedis           = "redis"
	sourceDNS             = "dns"
	sourceDNSNegative     = "dns_negative"
	sourceNetBIOS         = "netbios"
	sourceNetBIOSNegative = "netbios_negative"
)

// ResolveNow — БЛОКИРУЮЩИЙ резолв для фоновых задач (§106.3, дозаполнение
// client_host). В отличие от Lookup ждёт полную цепочку Redis → PTR →
// NetBIOS: иначе задача получала бы "" на каждом холодном IP.
//
// Свежая запись L1 отдаётся без I/O. Иначе цикл идёт через тот же
// singleflight, что и фоновые резолвы (одновременный Lookup того же IP
// второго запроса не породит), и заполняет оба кеша. Отменённый ctx даёт "" и
// ничего не кеширует. На пути доставки запроса не вызывать — для него Lookup.
func (r *Resolver) ResolveNow(ctx context.Context, ip string) string {
	if skipIP(ip) || ctx.Err() != nil {
		return ""
	}
	r.mu.Lock()
	e, cached := r.l1[ip]
	r.mu.Unlock()
	if cached && time.Now().Before(e.expiresAt) {
		return e.host
	}
	// Под singleflight ведущим оказывается первый вызов: присоединившийся
	// получает его результат, в том числе "" при отмене ведущего контекста.
	// Для суточной задачи это приемлемо — ничего не закешировано, IP спросят
	// в следующем цикле.
	v, _, _ := r.sf.Do(ip, func() (any, error) {
		return r.resolveOnce(ctx, ip), nil
	})
	host, _ := v.(string)
	return host
}

// resolveOnce выполняет полный цикл резолва и заполняет оба кеша.
func (r *Resolver) resolveOnce(ctx context.Context, ip string) string {
	start := time.Now()
	if ctx.Err() != nil {
		return "" // приложение останавливается или вызывающий отменил
	}
	if host, ok := r.fromRedis(ctx, ip); ok {
		return host
	}
	host, source, err := r.lookupChain(ctx, ip)
	if ctx.Err() != nil {
		// Пустой ответ из-за отмены — не факт об IP: в негативный кеш на
		// negative_ttl_sec он попасть не должен (§106.2). Раньше остановка
		// сервиса посреди PTR-запроса оставляла пустое имя в Redis.
		r.logger.Debug("rdns: resolve cancelled, not cached",
			r.logger.Str("ip", ip), r.logger.Str("source", source))
		return host
	}
	r.l1Set(ip, host, host != "")
	r.storeRedis(ctx, ip, host)
	r.logOutcome(ip, host, source, err, start)
	return host
}

// fromRedis читает общий кеш реплик. ok=false — промаха или сбой Redis:
// идём в DNS. Пустая строка — валидный негативный хит (промах отличается по
// redis.Nil).
func (r *Resolver) fromRedis(ctx context.Context, ip string) (string, bool) {
	if r.redis == nil {
		return "", false
	}
	rctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	val, err := r.redis.Get(rctx, keyPrefix+ip).Result()
	cancel()
	switch {
	case err == nil:
		r.l1Set(ip, val, val != "")
		r.logger.Debug("rdns: resolved from redis cache",
			r.logger.Str("ip", ip), r.logger.Str("host", val),
			r.logger.Str("source", sourceRedis))
		return val, true
	case !errors.Is(err, goredis.Nil):
		// §51.9: fail-open — Redis недоступен, идём в DNS напрямую.
		r.logger.Debug("rdns: redis get failed, falling back to dns",
			r.logger.Str("ip", ip), r.logger.Err(err))
	}
	return "", false
}

// lookupChain — PTR, затем (если этап включён и адрес IPv4) NetBIOS.
// Возвращает имя, источник для Debug-строки и ошибки этапов (nil у успеха).
func (r *Resolver) lookupChain(ctx context.Context, ip string) (string, string, error) {
	host, err := r.lookupPTR(ctx, ip)
	if host != "" {
		return host, sourceDNS, nil
	}
	if r.lookupNetBIOS == nil {
		return "", sourceDNSNegative, err
	}
	if parsed := net.ParseIP(ip); parsed == nil || parsed.To4() == nil {
		// §51.9: NetBIOS — только IPv4, IPv6-клиент остаётся без имени.
		r.logger.Debug("rdns: netbios skipped (not ipv4)", r.logger.Str("ip", ip))
		return "", sourceDNSNegative, err
	}
	r.logger.Debug("rdns: ptr empty, trying netbios", r.ptrAttrs(ip, err)...)
	name, nerr := r.lookupNetBIOS(ctx, ip)
	if nerr != nil || name == "" {
		return "", sourceNetBIOSNegative, errors.Join(err, nerr)
	}
	if r.netbiosSuffix != "" {
		name += "." + r.netbiosSuffix
	}
	return name, sourceNetBIOS, nil
}

// lookupPTR — PTR-запрос с таймаутом; "" — имени нет (NXDOMAIN, пустой ответ
// или ошибка DNS).
func (r *Resolver) lookupPTR(ctx context.Context, ip string) (string, error) {
	dctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	names, err := r.lookupAddr(dctx, ip)
	if err != nil || len(names) == 0 {
		return "", err
	}
	return strings.TrimSuffix(names[0], "."), nil
}

// storeRedis кладёт результат в общий кеш реплик: позитив — cache_ttl_sec,
// негатив — negative_ttl_sec. Сбой — Debug, не фатально.
func (r *Resolver) storeRedis(ctx context.Context, ip, host string) {
	if r.redis == nil {
		return
	}
	ttl := r.cfg.CacheTTL
	if host == "" {
		ttl = r.cfg.NegativeTTL
	}
	rctx, cancel := context.WithTimeout(ctx, redisOpTimeout)
	defer cancel()
	if err := r.redis.Set(rctx, keyPrefix+ip, host, ttl).Err(); err != nil {
		r.logger.Debug("rdns: redis set failed",
			r.logger.Str("ip", ip), r.logger.Err(err))
	}
}

// logOutcome — §51.9: исход резолва. Отсутствие имени — норма (Debug, не Warn).
func (r *Resolver) logOutcome(ip, host, source string, err error, start time.Time) {
	attrs := []slog.Attr{
		r.logger.Str("ip", ip), r.logger.Str("host", host),
		r.logger.Str("source", source),
		r.logger.Int("duration_ms", int(time.Since(start).Milliseconds())),
	}
	if err != nil {
		attrs = append(attrs, r.logger.Err(err))
	}
	r.logger.Debug("rdns: resolved", attrs...)
}

func (r *Resolver) ptrAttrs(ip string, err error) []slog.Attr {
	attrs := []slog.Attr{r.logger.Str("ip", ip)}
	if err != nil {
		attrs = append(attrs, r.logger.Err(err))
	}
	return attrs
}
