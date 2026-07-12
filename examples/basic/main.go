package main

import (
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"

	"github.com/janisto/echo-observability"
)

func setup(e *echo.Echo) (*zap.Logger, error) {
	logger, err := obs.NewLogger(obs.LoggerConfig{Preset: obs.PresetDefault})
	if err != nil {
		return nil, err
	}
	logger = logger.With(projectFields()...)
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{Logger: logger}),
		obs.AccessLogger(obs.AccessLoggerConfig{Logger: logger}),
		middleware.Recover(),
	)
	e.GET("/health", func(c *echo.Context) error {
		obs.Logger(c.Request().Context()).Info("health check")
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})
	return logger, nil
}

func main() {
	e := echo.New()
	logger, err := setup(e)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", e)
	registerHTTPRoutes(mux)
	handler := obs.HTTPRequestContext(obs.HTTPRequestContextConfig{Logger: logger})(mux)
	server := &http.Server{
		Addr:              ":" + envOrDefault("PORT", "8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	panic(server.ListenAndServe())
}

func registerHTTPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		obs.Logger(r.Context()).Info("readiness check")
	})
}

func projectFields() []zap.Field {
	return []zap.Field{
		zap.String("service", envOrDefault("SERVICE_NAME", "echo-example")),
		zap.String("environment", envOrDefault("SERVICE_ENV", "local")),
		zap.String("version", envOrDefault("SERVICE_VERSION", "dev")),
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
