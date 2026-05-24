package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"time"

	"bus/app/build"
	"bus/app/internal/app"
	"bus/app/internal/config"
	clientHttp "bus/app/internal/controller/http"
	"bus/app/internal/controller/manager"
	"bus/app/internal/handler"
	"bus/app/internal/lib/caching"
	"bus/app/internal/lib/caching/memory"
	"bus/app/internal/lib/caching/redis"
	"bus/app/internal/service"
	"bus/app/internal/service/example"
	"bus/app/internal/storage"
	"bus/app/internal/storage/database"
	"bus/app/internal/storage/migrations"
	"bus/app/internal/storage/repository"
	"bus/app/internal/terminal"

	"github.com/getsentry/sentry-go"
	"github.com/jinzhu/copier"
	svc "github.com/kardianos/service"
	"github.com/vsilchenkov/errors"
	"github.com/vsilchenkov/logging"
)

const projectName = "Bus"

var svcConfig = &svc.Config{
	Name:        projectName + "Service",
	DisplayName: projectName + " API Service",
	Description: "API-приложение " + projectName,
}

//go:embed versioninfo.json
var versionInfoData []byte

// @title Bus API
// @version 1.0
// @description Bus API Service
// @host localhost:8090
// @BasePath /

// @securityDefinitions.basic BasicAuth
// @description Basic authentication (username:password)

func main() {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := initOption()
	c := initConfig(b)

	sentryConfig := initSentry(c)
	if sentryConfig.Use {
		defer sentry.Flush(2 * time.Second)
	}

	logger := initLogger(c, sentryConfig)
	defer func() {
		if err := errors.PanicRecovered(recover()); err != nil {
			logger.Error("Panic recovered",
				logger.Err(err),
			)
			os.Exit(1)
		}
	}()

	db := initDB(ctx, c, logger)
	cacher := initCacher(c, logger)
	store := repository.NewRepository(db, cacher, logger)

	app := app.New(store, cacher, c, logger, cancel)

	if runTerminal(ctx, store, c, logger) {
		os.Exit(0)
	}

	RunServer(app, c, logger)

}

func RunServer(app app.App, c *config.Config, logger logging.Logger) {

	srv := clientHttp.New(app)

	svcExample := example.New(app)
	services := service.New(svcExample)

	hd := handler.New(services, app.Store, app.Cacher, app.Config, app.Logger).Init()
	man := manager.New(srv, hd, c.Server.Port, app)

	s, err := svc.New(man, svcConfig)
	if err != nil {
		logger.Error("Error on service start",
			logger.Err(err))
	}

	err = s.Run()
	if err != nil {
		logger.Error("Ошибка запуска сервера",
			logger.Err(err))
	}

}

func initOption() *build.Option {

	b, err := build.NewOption(versionInfoData)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	b.ProjectName = projectName
	return b
}

func initConfig(b *build.Option) *config.Config {

	c := config.New(*b)
	flags := config.ParseFlags()
	err := config.LoadSettigs(flags, c)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	return c

}

func initSentry(c *config.Config) *logging.SentryConfig {

	sentryConfig := &logging.SentryConfig{}
	copier.Copy(sentryConfig, c.Option)
	copier.Copy(sentryConfig, c.Sentry)

	if sentryConfig.Use {
		err := sentry.Init(logging.SentryClientOptions(sentryConfig))
		if err != nil {
			fmt.Printf("init sentry error: %v\n", err)
			os.Exit(1)
		}
	}
	return sentryConfig
}

func initLogger(c *config.Config, sentryConfig *logging.SentryConfig) logging.Logger {

	logConfig := &logging.Config{}
	copier.Copy(logConfig, c.Option)
	copier.Copy(logConfig, c.Log)
	logger := logging.Initlogger(logConfig, sentryConfig)
	return logger
}

func initDB(ctx context.Context, c *config.Config, logger logging.Logger) storage.DB {

	configDB := &storage.Config{
		DBType:   c.DataBase.Type,
		Host:     c.DataBase.Host,
		Port:     c.DataBase.Port,
		DBName:   c.DataBase.DBName,
		User:     c.DataBase.Credintials.UserName,
		Password: c.DataBase.Credintials.Password,
	}

	db, err := database.New(configDB, logger)
	if err != nil {
		logger.Error("error database open",
			logger.Err(err))
		os.Exit(1)
	}

	ctxDB, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctxDB); err != nil {
		logger.Error("database ping failed",
			logger.Err(err))
		os.Exit(1)
	}

	if c.Migrations.Up || c.Migrations.Down {
		runMigrations(db, c.Migrations, logger)
		os.Exit(0)
	}

	return db
}

func runMigrations(db *database.DataBase, c config.Migrations, logger logging.Logger) {

	var err error

	m, err := migrations.New(db, logger)
	if err != nil {
		logger.Error("Error creating migration instance", logger.Err(err))
		return
	}
	defer m.Close()

	switch {
	case c.Up:
		err = m.Up()
		if err != nil {
			logger.Error("Error running migrations up", logger.Err(err))
			return
		}
	case c.Down:
		err = m.Down()
		if err != nil {
			logger.Error("Error running migrations down", logger.Err(err))
			return

		}
	}
}

func initCacher(c *config.Config, logger logging.Logger) caching.Cacher {

	var cacher caching.Cacher

	useMemory := true
	if c.Redis.Use {
		cache, err := redis.New(&redis.Option{
			Addr:     c.Redis.Addr,
			Username: c.Redis.Credintials.UserName,
			Password: c.Redis.Credintials.Password,
			DB:       c.Redis.DB,
		})
		if err == nil {
			cacher = caching.New(cache)
			useMemory = false
		} else {
			logger.Error("Failed to initialize Redis cacher",
				logger.Err(err))
		}
	}

	if useMemory {
		cache := memory.New()
		cacher = caching.New(cache)
	}

	return cacher
}

func runTerminal(ctx context.Context, store repository.Repositorer, c *config.Config, logger logging.Logger) bool {

	if !c.Terminal.Run() {
		return false
	}

	t := terminal.New(store, logger)

	if c.Terminal.ChangeUserPassword {
		t.ChangeUserPassword(ctx)
	}

	return true
}
