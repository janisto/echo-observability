package main

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"

	"github.com/janisto/echo-observability"
)

func main() {
	logger, err := obs.NewLogger(obs.LoggerConfig{Preset: obs.PresetGCP})
	if err != nil {
		panic(err)
	}

	e := echo.New()
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{Logger: logger, Preset: obs.PresetGCP}),
		obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger, Preset: obs.PresetGCP}),
		middleware.Recover(),
	)
	e.GET("/health", func(c *echo.Context) error {
		obs.Logger(c.Request().Context()).Info("health check")
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})

	if err := e.Start(":8080"); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}
