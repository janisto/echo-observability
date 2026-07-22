package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	obs "github.com/janisto/echo-observability/v2"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.uber.org/zap"
)

type caseConfig struct {
	preset     obs.Preset
	traceLevel obs.TraceContextLevel
}

type traceBody struct {
	OK             bool   `json:"ok"`
	RequestID      string `json:"request_id"`
	CanaryReceived bool   `json:"canary_received"`
}

var nestedConfiguration = map[string]any{
	"system_id": "sys-402",
	"server_settings": map[string]any{
		"nodes": []map[string]any{{
			"hostname":    "srv-01",
			"port":        8080,
			"ssl_enabled": true,
		}},
	},
}

func main() {
	selected, err := configuredCase(requiredEnvironment("OBS_E2E_CASE"))
	if err != nil {
		log.Fatal(err)
	}
	canary := requiredEnvironment("OBS_E2E_SECRET_CANARY")
	port, err := configuredPort()
	if err != nil {
		log.Fatal(err)
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: selected.preset,
	})
	if err != nil {
		log.Fatal("logger configuration failed")
	}

	e := echo.New()
	accessConfig := obs.AccessLoggerConfig{
		Logger:            logger,
		Preset:            selected.preset,
		TraceContextLevel: selected.traceLevel,
	}
	if selected.preset == obs.PresetGCP {
		accessConfig.ExtraFields = func(*echo.Context) []zap.Field {
			return []zap.Field{zap.Any("e2e_configuration", nestedConfiguration)}
		}
	}
	e.Use(
		obs.RequestContext(obs.RequestContextConfig{
			Logger:            logger,
			Preset:            selected.preset,
			TraceContextLevel: selected.traceLevel,
		}),
		middleware.Recover(),
		obs.AccessLogger(accessConfig),
	)
	_, err = e.AddRoute(echo.Route{
		Method:  http.MethodGet,
		Path:    "/trace",
		Name:    "trace",
		Handler: traceHandler(canary),
	})
	if err != nil {
		log.Fatal("route configuration failed")
	}
	server := echo.StartConfig{
		Address:    fmt.Sprintf("0.0.0.0:%d", port),
		HideBanner: true,
		HidePort:   true,
	}
	if err := server.Start(context.Background(), e); err != nil && err != http.ErrServerClosed {
		log.Fatal("server failed")
	}
}

func traceHandler(canary string) echo.HandlerFunc {
	expected := []byte("Bearer " + canary)
	return func(c *echo.Context) error {
		if subtle.ConstantTimeCompare([]byte(c.Request().Header.Get("Authorization")), expected) != 1 {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
		ctx := c.Request().Context()
		obs.Logger(ctx).Info("handler", zap.String("event", "trace"))
		return c.JSON(http.StatusOK, traceBody{
			OK:             true,
			RequestID:      obs.RequestID(ctx),
			CanaryReceived: true,
		})
	}
}

func requiredEnvironment(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s must be nonempty", name)
	}
	return value
}

func configuredCase(name string) (caseConfig, error) {
	config := caseConfig{traceLevel: obs.TraceContextLevel1}
	switch name {
	case "common_level1":
		return config, nil
	case "common_level2":
		config.traceLevel = obs.TraceContextLevel2
		return config, nil
	case "aws_level1":
		config.preset = obs.PresetAWS
		return config, nil
	case "azure_level1":
		config.preset = obs.PresetAzure
		return config, nil
	case "gcp_level1":
		config.preset = obs.PresetGCP
		return config, nil
	default:
		return caseConfig{}, fmt.Errorf("unsupported OBS_E2E_CASE %q", name)
	}
}

func configuredPort() (int, error) {
	raw := os.Getenv("PORT")
	if raw == "" {
		raw = "8080"
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be between 1 and 65535")
	}
	return port, nil
}
