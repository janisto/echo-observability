package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability"
)

func TestHealthRouteEmitsCorrelatedApplicationAndAccessLogs(t *testing.T) {
	t.Parallel()
	recorder, records := serveHealth(t, zapcore.DebugLevel)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("response = (%d, %q), want (200, JSON ok)", recorder.Code, recorder.Body.String())
	}
	if len(records) != 3 {
		t.Fatalf("log count = %d, want info, debug, and access records: %#v", len(records), records)
	}

	assertFields(t, records[0], map[string]any{
		"severity": "INFO", "message": "health check",
		"service_name": "example-service", "health_status": "ok",
	})
	assertFields(t, records[1], map[string]any{
		"severity": "DEBUG", "message": "dependency check",
		"dependency": "database", "dependency_status": "ok", "check_duration_ms": float64(3),
	})
	for _, record := range records {
		assertFields(t, record, map[string]any{
			"request_id": "health-example", "correlation_id": "health-example",
		})
	}

	access := records[2]
	assertFields(t, access, map[string]any{
		"severity": "INFO", "message": "request completed",
		"method": "GET", "path": "/health", "path_template": "/health", "status": float64(200),
	})
	httpRequest, ok := access["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest = %#v, want GCP request object", access["httpRequest"])
	}
	assertFields(t, httpRequest, map[string]any{
		"requestMethod": "GET", "requestUrl": "http://example.com/health", "status": float64(200),
	})
	for _, applicationOnly := range []string{
		"service_name", "health_status", "dependency", "dependency_status", "check_duration_ms",
	} {
		if _, exists := access[applicationOnly]; exists {
			t.Fatalf("access record contains application-only field %q: %#v", applicationOnly, access)
		}
	}
}

func TestHealthRouteRespectsInfoLevel(t *testing.T) {
	t.Parallel()
	_, records := serveHealth(t, zapcore.InfoLevel)
	if len(records) != 2 {
		t.Fatalf("log count = %d, want info and access records: %#v", len(records), records)
	}
	if records[0]["message"] != "health check" || records[1]["message"] != "request completed" {
		t.Fatalf("unexpected info-level log sequence: %#v", records)
	}
	serialized, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte("dependency check")) ||
		bytes.Contains(serialized, []byte("check_duration_ms")) {
		t.Fatalf("debug-only data reached info-level output: %s", serialized)
	}
}

func serveHealth(t *testing.T, level zapcore.LevelEnabler) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var output bytes.Buffer
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset: obs.PresetGCP,
		Level:  level,
		Writer: &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logger.Sync(); err != nil {
			t.Errorf("sync logger: %v", err)
		}
	})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-ID", "health-example")
	recorder := httptest.NewRecorder()
	newApp(logger).ServeHTTP(recorder, request)
	return recorder, decodeRecords(t, output.String())
}

func decodeRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON log record: %v\n%s", err, line)
		}
		records = append(records, record)
	}
	return records
}

func assertFields(t *testing.T, record, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("%s = %#v, want %#v in %#v", key, record[key], value, record)
		}
	}
}
