package logging

import (
	"github.com/vsilchenkov/logging"
)

type Logger = logging.Logger

func InitLogger(c *logging.Config, sConfig *logging.SentryConfig) Logger {
	return logging.Initlogger(c, sConfig)
}

func GetLogger() Logger {
	return logging.GetLogger()
}
