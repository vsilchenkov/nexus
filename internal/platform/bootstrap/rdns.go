package bootstrap

import (
	"context"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"nexus/internal/platform/config"
	"nexus/internal/platform/logging"
	"nexus/internal/platform/netbios"
	"nexus/internal/platform/rdns"
)

// NewHostResolver собирает резолвер имени клиента (§67) по секции
// sender.rdns — одну и ту же для Sender'а (client_host в логах узлов) и
// Receiver'а (журнал отказов §94): кеш имён у них общий (nexus:rdns:<ip> в
// Redis), и два разных набора настроек для одного кеша только разошлись бы.
//
// nil — резолв выключен (sender.rdns.disabled). Вызывающий обязан проверить
// nil до передачи в опцию с интерфейсом: иначе typed-nil *rdns.Resolver
// станет «непустым» интерфейсом и упадёт на первом Lookup.
//
// При sender.rdns.netbios.enabled подключается второй этап (§106.2): NetBIOS
// Node Status для IPv4-клиентов без PTR. redis может быть nil (fail-open:
// режим «только L1»).
func NewHostResolver(ctx context.Context, redis *goredis.Client, cfg *config.SenderRDNSConfig, logger logging.Logger) *rdns.Resolver {
	if cfg.Disabled {
		return nil
	}
	var opts []rdns.Option
	if nb := cfg.NetBIOS; nb.Enabled {
		client := netbios.New(netbios.Config{
			Port:    nb.Port,
			Timeout: time.Duration(nb.TimeoutMs) * time.Millisecond,
		}, logger)
		opts = append(opts, rdns.WithNetBIOS(client.Hostname, nb.Suffix))
		logger.Info("rdns: netbios stage enabled",
			logger.Int("port", nb.Port), logger.Int("timeout_ms", nb.TimeoutMs),
			logger.Str("suffix", nb.Suffix))
	}
	return rdns.New(ctx, redis, rdns.Config{
		Timeout:     time.Duration(cfg.TimeoutMs) * time.Millisecond,
		CacheTTL:    time.Duration(cfg.CacheTTLSec) * time.Second,
		NegativeTTL: time.Duration(cfg.NegativeTTLSec) * time.Second,
	}, logger, opts...)
}
