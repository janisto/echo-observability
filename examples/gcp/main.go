package main

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability/v2"
)

func main() {
	profileVersion, err := obs.ResolveGCPProfileVersion(obs.PresetGCP, "")
	if err != nil {
		panic(err)
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset:            obs.PresetGCP,
		GCPProfileVersion: profileVersion,
		Level:             zapcore.DebugLevel,
	})
	if err != nil {
		panic(err)
	}

	e, err := newApp(logger, profileVersion, nil)
	if err != nil {
		panic(err)
	}
	if err := e.Start(":8080"); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func newApp(logger *zap.Logger, profileVersion obs.GCPProfileVersion, now func() time.Time) (*echo.Echo, error) {
	return newAppWithPreset(logger, obs.PresetGCP, profileVersion, now)
}

func newAppWithPreset(
	logger *zap.Logger,
	preset obs.Preset,
	profileVersion obs.GCPProfileVersion,
	now func() time.Time,
) (*echo.Echo, error) {
	e := echo.New()
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: preset}),
		middleware.Recover(),
		obs.AccessLogger(obs.AccessLoggerConfig{
			Logger:            logger,
			Preset:            preset,
			GCPProfileVersion: profileVersion,
			Now:               now,
		}),
	)
	_, err := e.AddRoute(echo.Route{
		Method:  http.MethodGet,
		Path:    "/health",
		Name:    "health_check",
		Handler: health,
	})
	return e, err
}

func health(c *echo.Context) error {
	logger := obs.Logger(c.Request().Context())
	logger.Info("health check",
		zap.String("service_name", "example-service"),
		zap.String("service_version", "1.0.0"),
		zap.String("health_status", "ok"),
	)
	logger.Debug("dependency check",
		zap.String("dependency", "database"),
		zap.String("dependency_status", "ok"),
		zap.Int64("check_duration_ms", 3),
	)
	return c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
