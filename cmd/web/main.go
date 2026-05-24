// Web Service — REST API админки + SPA через embed.FS.
// См. §7, §11, §17.1 ТЗ и Phase 0 плана (только healthcheck + metrics).
package main

import (
	"context"
	_ "embed"
	"os"

	"bus/internal/platform/bootstrap"
	"bus/internal/platform/runner"
	"bus/internal/web"
)

const (
	projectName = "Web"
	serviceName = "DataBusWebService"
	displayName = "DataBus Web Service"
	description = "Web service of the DataBus — admin REST API and embedded SPA."
)

//go:embed versioninfo.json
var versionInfoData []byte

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, flags, cfg, logger := bootstrap.Init(versionInfoData, projectName)
	defer bootstrap.Shutdown(logger)

	if bootstrap.HandleMigrateFlags(flags, cfg, logger) {
		return
	}

	pgPool := bootstrap.MustPG(ctx, cfg, logger)
	defer pgPool.Close()

	redisClient := bootstrap.MustRedis(ctx, cfg, logger)
	defer redisClient.Close()

	app := web.New(cfg, pgPool, redisClient, logger)

	if err := runner.Run(serviceName, displayName, description, app, logger); err != nil {
		logger.ErrorWithOp("service stopped", err, "main")
		os.Exit(1)
	}
}
