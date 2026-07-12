package applog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability"
)

func TestHelpersUseRequestScopedLoggerAndMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := obs.NewLogger(obs.LoggerConfig{Writer: &buffer, Level: zapcore.DebugLevel})
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	e.Use(obs.RequestContext(obs.RequestContextConfig{Logger: logger}))
	e.GET("/wrapper", func(c *echo.Context) error {
		ctx := c.Request().Context()
		Debug(ctx, "debug helper", zap.String("debug_field", "yes"))
		Info(ctx, "info helper", zap.String("info_field", "yes"))
		Warn(ctx, "warn helper", zap.String("warn_field", "yes"))
		Error(ctx, "error helper", errors.New("boom"), zap.String("error_field", "yes"))
		Log(ctx, zapcore.WarnLevel, "log helper", zap.String("log_field", "yes"))
		return c.NoContent(http.StatusOK)
	})
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wrapper", nil)
	req.Header.Set("X-Request-Id", "req-wrapper")
	req.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	e.ServeHTTP(httptest.NewRecorder(), req)
	logs := decodeLogLines(t, buffer.String())
	assertLog(t, logs, "debug helper", "DEBUG", map[string]any{
		"request_id": "req-wrapper", "correlation_id": traceID, "trace_id": traceID,
		"trace_sampled": true, "debug_field": "yes",
	})
	assertLog(t, logs, "info helper", "INFO", map[string]any{"info_field": "yes"})
	assertLog(t, logs, "warn helper", "WARN", map[string]any{"warn_field": "yes"})
	assertLog(t, logs, "error helper", "ERROR", map[string]any{"error": "boom", "error_field": "yes"})
	assertLog(t, logs, "log helper", "WARN", map[string]any{"log_field": "yes"})
}

func TestWithErrorPrependsWithoutMutatingFields(t *testing.T) {
	t.Parallel()
	fields := []zap.Field{zap.String("component", "worker")}
	got := withError(errors.New("boom"), fields)
	if len(got) != 2 || got[0].Key != "error" || got[1].Key != "component" || fields[0].Key != "component" {
		t.Fatalf("fields = %#v input = %#v", got, fields)
	}
	withoutErr := withError(nil, fields)
	if len(withoutErr) != 1 || withoutErr[0].Key != "component" {
		t.Fatalf("without error = %#v", withoutErr)
	}
}

func decodeLogLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	logs := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		logs = append(logs, entry)
	}
	return logs
}

func assertLog(t *testing.T, logs []map[string]any, msg, level string, fields map[string]any) {
	t.Helper()
	for _, entry := range logs {
		if entry["message"] != msg {
			continue
		}
		if entry["level"] != level {
			t.Fatalf("%s level = %v", msg, entry["level"])
		}
		for key, want := range fields {
			if entry[key] != want {
				t.Fatalf("%s %s = %#v, want %#v", msg, key, entry[key], want)
			}
		}
		return
	}
	t.Fatalf("message %q missing from %#v", msg, logs)
}
