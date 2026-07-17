package main

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability"
)

func main() {
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetGCP,
		Level:  zapcore.DebugLevel,
	})
	if err != nil {
		panic(err)
	}

	e := newApp(logger)
	if err := e.Start(":8080"); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func newApp(logger *zap.Logger) *echo.Echo {
	e := echo.New()
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: obs.PresetGCP}),
		obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger, Preset: obs.PresetGCP}),
		middleware.Recover(),
	)
	e.GET("/health", health)
	return e
}

func health(c *echo.Context) error {
	logger := obs.Logger(c.Request().Context())
	logger.Info("health check",
		zap.String("service_name", "example-service"),
		zap.String("health_status", "ok"),
	)
	logger.Debug("dependency check",
		zap.String("dependency", "database"),
		zap.String("dependency_status", "ok"),
		zap.Int64("check_duration_ms", 3),
	)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
