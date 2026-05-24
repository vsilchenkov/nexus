package handler

import (
	"context"
	"fmt"
	"os"

	"bus/app/internal/config"
	"bus/app/internal/lib/caching"
	"bus/app/internal/lib/logging"
	"bus/app/internal/service"
	"bus/app/internal/storage/repository"
	"bus/docs"

	"github.com/getsentry/sentry-go"
	sentrygin "github.com/getsentry/sentry-go/gin"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	vslogging "github.com/vsilchenkov/logging"
)

var sentrySkipPaths = map[string]struct{}{
	"/metrics": {},
}

type Handler struct {
	ctx      context.Context
	services *service.Services
	store    repository.Repositorer
	cacher   caching.Cacher
	config   *config.Config
	logger   logging.Logger
}

func New(ctx context.Context,
	services *service.Services,
	store repository.Repositorer,
	cacher caching.Cacher,
	c *config.Config,
	logger logging.Logger) *Handler {

	return &Handler{
		ctx:      ctx,
		services: services,
		store:    store,
		cacher:   cacher,
		config:   c,
		logger:   logger}
}

func (h *Handler) Init() *gin.Engine {

	docs.SwaggerInfo.Title = fmt.Sprintf("%s %s", h.config.StringFileInfo.ProductName, h.config.Sentry.Environment)
	docs.SwaggerInfo.Host = "localhost:" + h.config.Server.Port
	docs.SwaggerInfo.BasePath = "/"

	debug := h.config.UseDebug()
	if !debug {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())

	if debug {
		router.Use(cors.New(cors.Config{
			AllowOrigins: []string{"*"},
			AllowMethods: []string{"*"},
			AllowHeaders: []string{"*"},
		}))
		router.Use(h.loggerMW())
	} else {
		if h.config.Interactive {
			router.Use(gin.Logger())
		}
	}

	if h.config.Sentry.Use {
		if err := h.initSentry(); err == nil {
			router.Use(h.sentryMW())
		} else {
			h.logger.Error("Sentry initialization failed",
				h.logger.Err(err))
		}
	}

	router.GET("/", h.swagger)
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	api := router.Group("/api")
	api.GET("/", h.swagger)
	{
		api.GET("/ping", h.ping)
	}

	return router

}

func (h *Handler) loggerMW() gin.HandlerFunc {

	var out *os.File
	out = os.Stdout

	settngs := h.config
	if settngs.Log.OutputInFile {
		fileName := "api.log"
		file, err := vslogging.GetOutputLogFile(settngs.WorkingDir, settngs.Log.Dir, fileName)
		if err == nil {
			out = file
		} else {
			h.logger.Error("Не удалось открыть файл логов, используется стандартный stderr",
				h.logger.Str("name", fileName),
				h.logger.Err(err))
		}
	}

	custumlogger := gin.LoggerWithWriter(out)
	return custumlogger
}

func (h *Handler) initSentry() error {

	sentryConfig := &vslogging.SentryConfig{}
	copier.Copy(sentryConfig, h.config.Option)
	copier.Copy(sentryConfig, h.config.Sentry)

	return sentry.Init(vslogging.SentryClientOptions(sentryConfig))

}

func (h *Handler) sentryMW() gin.HandlerFunc {

	sentryMW := sentrygin.New(sentrygin.Options{
		Repanic: true,
	})

	return func(c *gin.Context) {
		if _, skip := sentrySkipPaths[c.Request.URL.Path]; skip {
			c.Next()
			return
		}
		sentryMW(c)
	}
}
