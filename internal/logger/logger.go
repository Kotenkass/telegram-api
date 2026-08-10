package logger

import (
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	// ServiceName is the default service_name value emitted in every JSON log entry.
	ServiceName = "[service-name]"

	EnvLogLevel = "LOG_LEVEL"
)

// NewLogger creates a production-oriented JSON logrus logger.
//
// The log level is read from LOG_LEVEL. Supported values are debug, info, and error.
// Invalid or missing values fall back to info.
func NewLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
	})
	logger.SetLevel(parseLevel(os.Getenv(EnvLogLevel)))
	logger.SetReportCaller(false)
	logger.AddHook(&serviceHook{service: ServiceName})

	return logger
}

func parseLevel(value string) logrus.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return logrus.DebugLevel
	case "error":
		return logrus.ErrorLevel
	case "info", "":
		return logrus.InfoLevel
	default:
		return logrus.InfoLevel
	}
}

type serviceHook struct {
	service string
}

func (h *serviceHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *serviceHook) Fire(entry *logrus.Entry) error {
	entry.Data["service_name"] = h.service
	return nil
}
