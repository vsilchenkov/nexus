// Sender Service — внутренний сервис доставки.
// См. §4 ТЗ и Phase 0 плана (gRPC healthcheck + admin HTTP).
package main

import (
	"context"
	_ "embed"
	"os"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/runner"
	"nexus/internal/sender"
)

const (
	projectName = "Sender"
	serviceName = "NexusSenderService"
	displayName = "Nexus Sender Service"
	description = "Sender service of the Nexus — performs outbound HTTP calls to external nodes, consumes Kafka async queue."
)

//go:embed versioninfo.json
var versionInfoData []byte

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, flags, cfg, logger, logCtl := bootstrap.Init(versionInfoData, projectName)
	defer bootstrap.Shutdown(logger)

	if bootstrap.HandleMigrateFlags(flags, cfg, logger) {
		return
	}

	pgPool := bootstrap.MustPG(ctx, cfg, logger)
	defer pgPool.Close()

	// §70.5: сверяем идентификатор ноды. Sender миграции не накатывает, поэтому
	// отсутствие instance_identity здесь не фатально — он стартовал раньше Web.
	identity := bootstrap.MustInstanceIdentity(ctx, pgPool, cfg, flags, projectName, logger)

	// Cipher нужен раньше overlay'я: тот расшифровывает секреты app_settings (§90.1).
	cipher := bootstrap.MustCipher(logger)

	// §8.4 / §14.5: накладываем CH-настройки из app_settings ДО подключения.
	bootstrap.ApplyAppSettings(ctx, pgPool, cfg, cipher, logger)

	// Conn закрывается через sender.App.Stop → clickhouse.Manager.Close,
	// чтобы при hot-reload (§8.4) закрылся ТЕКУЩИЙ conn, а не исходный
	// (который мог быть уже swap'нут).
	chConn := bootstrap.MustClickHouse(ctx, cfg, logger)

	redisClient := bootstrap.MustRedis(ctx, cfg, logger)
	defer redisClient.Close()

	// §3.6: nexus.async.paused — delay-топик отложенных сообщений paused-узлов.
	// §38: nexus.logs.retry — durable-буфер проваленных CH-батчей (если задан).
	topics := []string{cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic, cfg.Kafka.PausedTopic}
	if cfg.Kafka.RetryTopic != "" {
		topics = append(topics, cfg.Kafka.RetryTopic)
	}
	bootstrap.MustEnsureKafkaTopics(ctx, cfg, logger, topics...)

	otelShutdown := bootstrap.MustOtel(ctx, cfg, "sender", logger)

	app := sender.New(cfg, pgPool, chConn, redisClient, cipher, otelShutdown, identity, logger, logCtl)

	if err := runner.Run(serviceName, displayName, description, app, logger); err != nil {
		logger.ErrorWithOp("service stopped", err, "main")
		os.Exit(1)
	}
}
