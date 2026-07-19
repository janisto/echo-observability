package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/janisto/echo-observability"
)

func TestHealthRouteEmitsCorrelatedApplicationAndAccessLogs(t *testing.T) {
	t.Parallel()
	recorder, records := serveHealth(t, obs.PresetGCP, zapcore.DebugLevel)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("response = (%d, %q), want (200, JSON ok)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "health-example" {
		t.Fatalf("response request ID = %q, want health-example", got)
	}
	if len(records) != 3 {
		t.Fatalf("log count = %d, want info, debug, and access records: %#v", len(records), records)
	}

	assertFields(t, records[0], map[string]any{
		"severity": "INFO", "message": "health check",
		"service_name": "example-service", "service_version": "1.0.0", "health_status": "ok",
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
		"method": "GET", "duration_ms": float64(12.5), "path_template": "/health",
		"operation_id": "health_check", "status": float64(200),
	})
	for _, key := range []string{"path", "peer_ip", "remote_ip", "user_agent"} {
		if _, ok := access[key]; ok {
			t.Fatalf("access contains privacy field %q: %#v", key, access)
		}
	}
	httpRequest, ok := access["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest = %#v, want GCP request object", access["httpRequest"])
	}
	assertFields(t, httpRequest, map[string]any{
		"requestMethod": "GET", "status": float64(200), "latency": "0.0125s",
	})
	for _, key := range []string{"requestUrl", "remoteIp", "userAgent"} {
		if _, ok := httpRequest[key]; ok {
			t.Fatalf("httpRequest contains privacy field %q: %#v", key, httpRequest)
		}
	}
	if len(httpRequest) != 3 {
		t.Fatalf("httpRequest = %#v, want exact privacy-safe projection", httpRequest)
	}
	for _, applicationOnly := range []string{
		"service_name", "service_version", "health_status", "dependency", "dependency_status", "check_duration_ms",
	} {
		if _, exists := access[applicationOnly]; exists {
			t.Fatalf("access record contains application-only field %q: %#v", applicationOnly, access)
		}
	}
}

func TestHealthRouteRespectsInfoLevel(t *testing.T) {
	t.Parallel()
	_, records := serveHealth(t, obs.PresetGCP, zapcore.InfoLevel)
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

func TestCoreHealthRouteHasExactPortableProjection(t *testing.T) {
	for _, test := range []struct {
		name         string
		level        zapcore.Level
		wantMessages []string
	}{
		{name: "debug", level: zapcore.DebugLevel, wantMessages: []string{"health check", "dependency check", "request completed"}},
		{name: "info", level: zapcore.InfoLevel, wantMessages: []string{"health check", "request completed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder, records := serveHealth(t, obs.PresetDefault, test.level)
			if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"ok\":true}\n" {
				t.Fatalf("response = (%d, %q), want (200, JSON ok)", recorder.Code, recorder.Body.String())
			}
			if got := recorder.Header().Get("X-Request-Id"); got != "health-example" {
				t.Fatalf("response request ID = %q, want health-example", got)
			}
			if got := len(records); got != len(test.wantMessages) {
				t.Fatalf("log count = %d, want %d: %#v", got, len(test.wantMessages), records)
			}
			for index, message := range test.wantMessages {
				assertFields(t, records[index], map[string]any{
					"message": message, "request_id": "health-example", "correlation_id": "health-example",
				})
				wantLevel := "INFO"
				if message == "dependency check" {
					wantLevel = "DEBUG"
				}
				assertFields(t, records[index], map[string]any{"level": wantLevel})
				if _, ok := records[index]["severity"]; ok {
					t.Fatalf("core record contains GCP severity: %#v", records[index])
				}
			}
			assertFields(t, records[0], map[string]any{
				"service_name": "example-service", "service_version": "1.0.0", "health_status": "ok",
			})
			access := records[len(records)-1]
			assertFields(t, access, map[string]any{
				"method": "GET", "duration_ms": float64(12.5), "path_template": "/health",
				"operation_id": "health_check", "status": float64(200),
			})
			if _, ok := access["httpRequest"]; ok {
				t.Fatalf("core access record contains GCP httpRequest: %#v", access)
			}
			for _, key := range []string{"path", "peer_ip", "remote_ip", "user_agent"} {
				if _, ok := access[key]; ok {
					t.Fatalf("core access contains privacy field %q: %#v", key, access)
				}
			}
		})
	}
}

func serveHealth(
	t *testing.T,
	preset obs.Preset,
	level zapcore.LevelEnabler,
) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var output bytes.Buffer
	var profileVersion obs.GCPProfileVersion
	if preset == obs.PresetGCP {
		profileVersion = obs.GCPProfileVersionV0_1_0
	}
	logger, err := obs.NewLogger(obs.LoggerConfig{
		Preset:            preset,
		GCPProfileVersion: profileVersion,
		Level:             level,
		Writer:            &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if syncErr := logger.Sync(); syncErr != nil {
			t.Errorf("sync logger: %v", syncErr)
		}
	})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-ID", "health-example")
	recorder := httptest.NewRecorder()
	app, err := newAppWithPreset(logger, preset, profileVersion, fixedGCPHealthClock())
	if err != nil {
		t.Fatal(err)
	}
	app.ServeHTTP(recorder, request)
	return recorder, decodeRecords(t, output.String())
}

func fixedGCPHealthClock() func() time.Time {
	values := []time.Time{time.Unix(1, 0), time.Unix(1, int64(12_500*time.Microsecond))}
	index := 0
	return func() time.Time {
		value := values[index]
		index++
		return value
	}
}

func decodeRecords(t *testing.T, output string) []map[string]any {
	t.Helper()
	if output == "" || !strings.HasSuffix(output, "\n") {
		t.Fatalf("stdout is not non-empty LF-terminated NDJSON: %q", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" || strings.HasSuffix(line, "\r") {
			t.Fatalf("stdout contains an empty or CRLF NDJSON line: %q", output)
		}
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
