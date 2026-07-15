// Web Service — REST API админки + SPA через embed.FS.
// См. §7, §11, §17.1 ТЗ.
//
// @title         Nexus Web API
// @version       1.0
// @description   Admin REST API шины данных (§7 ТЗ). Сессии в Redis +
// @description   API-токены (Bearer db_*). SPA по embed.FS отдаётся
// @description   фолбэком на index.html для всех путей не из /api/.
// @basePath      /
// @schemes       http https
// @securityDefinitions.apikey  CookieAuth
// @in            cookie
// @name          nexus_session
// @securityDefinitions.apikey  ApiTokenAuth
// @in            header
// @name          Authorization
// @description   "Bearer db_<32-байт-base64>". См. §7.14.
package main

import (
	"context"
	_ "embed"
	"os"

	"nexus/internal/platform/bootstrap"
	"nexus/internal/platform/runner"
	"nexus/internal/web"
)

const (
	projectName = "Web"
	serviceName = "NexusWebService"
	displayName = "Nexus Web Service"
	description = "Web service of the Nexus — admin REST API and embedded SPA."
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
	if bootstrap.HandleSetAdminPassword(ctx, flags, cfg, logger) {
		return
	}

	pgPool := bootstrap.MustPG(ctx, cfg, logger)
	defer pgPool.Close()

	bootstrap.AutoMigrate(cfg, logger)
	// §8.4 / §14.5: накладываем dynamic-настройки из app_settings поверх
	// env-конфига до подключения зависимостей (CH-клиент возьмёт overlay'нутый адрес).
	bootstrap.ApplyAppSettings(ctx, pgPool, cfg, logger)

	redisClient := bootstrap.MustRedis(ctx, cfg, logger)
	defer redisClient.Close()

	// ClickHouse нужен для replay (§7.4.1) и live-tail (§7.4). Если недоступен
	// — Web стартует, но эти функции вернут 404 на свои эндпоинты.
	// Conn закрывается через web.App.Stop → clickhouse.Manager.Close,
	// чтобы при hot-reload (Phase 6.3.2.5) закрылся текущий conn, а не исходный.
	chConn, _ := bootstrap.TryClickHouse(ctx, cfg, logger)

	cipher := bootstrap.MustCipher(logger)

	otelShutdown := bootstrap.MustOtel(ctx, cfg, "web", logger)

	app := web.New(cfg, pgPool, redisClient, chConn, cipher, otelShutdown, logger, logCtl)

	if err := runner.Run(serviceName, displayName, description, app, logger); err != nil {
		logger.ErrorWithOp("service stopped", err, "main")
		os.Exit(1)
	}
}
