// Receiver Service — публичный HTTP-вход шины данных.
// См. §3 ТЗ и Phase 0 плана (только healthcheck + metrics).
//
// @title         Nexus Receiver API
// @version       1.0
// @description   Публичный HTTP-вход шины данных (§3 ТЗ). Принимает входящие
// @description   запросы клиентов, маршрутизирует по конфигу узла (path) и
// @description   проксирует на внешний адрес синхронно (/api/v1/request) либо
// @description   ставит в очередь Kafka асинхронно (/api/v1/requestAsync).
// @description   Префикс /api/v1/ обязателен. Контракт зависит от конфигурации
// @description   конкретного узла в админке.
// @basePath      /
// @schemes       http https
package main

import (
	"context"
	_ "embed"
	"os"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/runner"
	"nexus/internal/receiver"
)

const (
	projectName = "Receiver"
	serviceName = "NexusReceiverService"
	displayName = "Nexus Receiver Service"
	description = "Receiver service of the Nexus — accepts incoming HTTP requests, routes by node config, proxies to Sender via gRPC or to Kafka."
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

	// §74.3: схема новее этого бинаря старт не прекращает (незавершённый откат
	// кода) — состояние доезжает до метрик через App.
	schema := bootstrap.AutoMigrate(cfg, logger)

	pgPool := bootstrap.MustPG(ctx, cfg, logger)
	defer pgPool.Close()

	// §70.5: Receiver в ClickHouse не ходит, но сверяет идентификатор ноды —
	// подменённый конфиг (чужой instance.id при своей PostgreSQL) обязан валить
	// старт здесь, а не всплывать позже расхождением имён БД у Web и Sender.
	bootstrap.MustInstanceIdentity(ctx, pgPool, cfg, flags, projectName, logger)

	// §8.4 / §14.5: динамическая часть Sentry/CH из app_settings поверх env.
	bootstrap.ApplyAppSettings(ctx, pgPool, cfg, logger)

	redisClient := bootstrap.MustRedis(ctx, cfg, logger)
	defer redisClient.Close()

	cipher := bootstrap.MustCipher(logger)

	// PausedTopic (§3.6) создаём и здесь: Receiver может стартовать раньше
	// Sender'а, а Web читает delay-топик для вкладки «Очередь» (§35).
	bootstrap.MustEnsureKafkaTopics(ctx, cfg, logger,
		cfg.Kafka.AsyncTopic, cfg.Kafka.DLQTopic, cfg.Kafka.PausedTopic)

	otelShutdown := bootstrap.MustOtel(ctx, cfg, "receiver", logger)

	app := receiver.New(cfg, pgPool, redisClient, cipher, otelShutdown, schema, logger, logCtl)

	if err := runner.Run(serviceName, displayName, description, app, logger); err != nil {
		logger.ErrorWithOp("service stopped", err, "main")
		os.Exit(1)
	}
}
