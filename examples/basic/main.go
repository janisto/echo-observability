package main

import (
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"

	"github.com/janisto/echo-observability/v2"
)

func main() {
	logger, err := obs.NewLogger(obs.LoggerConfig{})
	if err != nil {
		panic(err)
	}

	e, err := newBasicApp(logger)
	if err != nil {
		panic(err)
	}

	if err := e.Start(":8080"); err != nil {
		logger.Error("server stopped", zap.Error(err))
	}
}

func newBasicApp(logger *zap.Logger) (*echo.Echo, error) {
	return newConfiguredApp(
		obs.RequestContextConfig{Logger: logger},
		obs.AccessLoggerConfig{Logger: logger},
	)
}

func newLevel2App(logger *zap.Logger) (*echo.Echo, error) {
	const traceContextLevel = obs.TraceContextLevel2
	return newConfiguredApp(
		obs.RequestContextConfig{Logger: logger, TraceContextLevel: traceContextLevel},
		obs.AccessLoggerConfig{Logger: logger, TraceContextLevel: traceContextLevel},
	)
}

func newConfiguredApp(
	requestContextConfig obs.RequestContextConfig,
	accessLoggerConfig obs.AccessLoggerConfig,
) (*echo.Echo, error) {
	e := echo.New()
	e.Use(
		obs.RequestContext(requestContextConfig),
		middleware.Recover(),
		obs.AccessLogger(accessLoggerConfig),
	)
	_, err := e.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/health",
		Name:   "health_check",
		Handler: func(c *echo.Context) error {
			obs.Logger(c.Request().Context()).Info("health check")
			return c.JSON(http.StatusOK, map[string]bool{"ok": true})
		},
	})
	return e, err
}
