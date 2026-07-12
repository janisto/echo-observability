package obs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type testStatusError struct {
	status int
}

func (e *testStatusError) Error() string {
	return http.StatusText(e.status)
}

func (e *testStatusError) StatusCode() int {
	return e.status
}

func TestAccessLoggerRoutePatternAndRequestContext(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(
		RequestContext(RequestContextConfig{Logger: logger}),
		AccessLogger(AccessLoggerConfig{
			Logger: logger,
			Now: fixedClock(
				time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 12, 12, 0, 0, int(250*time.Millisecond), time.UTC),
			),
			ExtraFields: func(c *echo.Context) []zap.Field {
				return []zap.Field{zap.String("tenant_id", c.Request().Header.Get("X-Tenant"))}
			},
		}),
	)
	_, err := e.AddRoute(echo.Route{
		Method: http.MethodGet,
		Path:   "/users/:id",
		Name:   "get-user",
		Handler: func(c *echo.Context) error {
			if RequestID(c.Request().Context()) != "req-route" || Logger(c.Request().Context()) == nil {
				t.Fatal("request context was not installed")
			}
			return c.JSON(http.StatusCreated, map[string]string{"id": c.Param("id")})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/users/123?expand=true", nil)
	req.RemoteAddr = "203.0.113.9:4321"
	req.Header.Set(defaultRequestIDHeader, "req-route")
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("X-Tenant", "tenant-1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"level": "INFO", "request_id": "req-route", "method": "GET",
		"path": "/users/123", "path_template": "/users/:id", "operation_id": "get-user",
		"status": float64(http.StatusCreated), "duration_ms": float64(250),
		"remote_ip": "203.0.113.9", "user_agent": "test-agent", "tenant_id": "tenant-1",
	})
}

func TestAccessLoggerUsesEchoIPExtractor(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.IPExtractor = func(*http.Request) string { return "198.51.100.42" }
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:4321"
	e.ServeHTTP(httptest.NewRecorder(), req)
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["remote_ip"] != "198.51.100.42" {
		t.Fatalf("remote_ip = %v, want configured Echo IP", entry["remote_ip"])
	}
}

func TestAccessLoggerHandlerAndAccessLinesUseInstalledLogger(t *testing.T) {
	t.Parallel()
	var requestBuffer, fallbackBuffer bytes.Buffer
	requestLogger := newTestLogger(t, LoggerConfig{Writer: &requestBuffer})
	fallbackLogger := newTestLogger(t, LoggerConfig{Writer: &fallbackBuffer})
	e := echo.New()
	e.Use(
		RequestContext(
			RequestContextConfig{Logger: requestLogger.With(zap.String("logger_source", "request-context"))},
		),
		AccessLogger(AccessLoggerConfig{
			Logger:      fallbackLogger.With(zap.String("logger_source", "access-config")),
			ExtraFields: func(*echo.Context) []zap.Field { return []zap.Field{zap.String("tenant_id", "tenant-1")} },
		}),
	)
	e.GET("/widgets/:id", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("handler log", zap.String("widget_id", c.Param("id")))
		return c.NoContent(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/widgets/123", nil)
	req.Header.Set(defaultRequestIDHeader, "req-installed")
	e.ServeHTTP(httptest.NewRecorder(), req)
	lines := decodeLogLines(t, requestBuffer.String())
	if len(lines) != 2 || fallbackBuffer.Len() != 0 {
		t.Fatalf("request lines=%#v fallback=%q", lines, fallbackBuffer.String())
	}
	for _, entry := range lines {
		assertFields(t, entry, map[string]any{
			"logger_source": "request-context", "request_id": "req-installed", "correlation_id": "req-installed",
		})
	}
	if lines[0]["widget_id"] != "123" {
		t.Fatalf("handler entry = %#v", lines[0])
	}
	if _, ok := lines[0]["tenant_id"]; ok {
		t.Fatalf("handler log included access-only field: %#v", lines[0])
	}
	if lines[1]["tenant_id"] != "tenant-1" {
		t.Fatalf("access entry = %#v", lines[1])
	}
}

func TestAccessLoggerEchoErrorStatusAndPropagation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		handler   echo.HandlerFunc
		wantCode  int
		wantLevel string
		wantError bool
	}{
		{
			"http error",
			func(*echo.Context) error { return echo.NewHTTPError(http.StatusBadRequest, "bad input") },
			http.StatusBadRequest,
			"WARN",
			true,
		},
		{
			"plain error",
			func(*echo.Context) error { return errors.New("database unavailable") },
			http.StatusInternalServerError,
			"ERROR",
			true,
		},
		{
			"custom status error",
			func(*echo.Context) error { return &testStatusError{status: http.StatusUnprocessableEntity} },
			http.StatusUnprocessableEntity,
			"WARN",
			true,
		},
		{
			"success",
			func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) },
			http.StatusNoContent,
			"INFO",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
			e := echo.New()
			e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
			e.GET("/test", tt.handler)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("wire status = %d, want %d", rec.Code, tt.wantCode)
			}
			entry := decodeSingleLogLine(t, buffer.String())
			assertFields(t, entry, map[string]any{"status": float64(tt.wantCode), "level": tt.wantLevel})
			_, hasError := entry["error"]
			if hasError != tt.wantError {
				t.Fatalf("error field present = %v, want %v: %#v", hasError, tt.wantError, entry)
			}
		})
	}
}

func TestAccessLoggerCommittedStatusWinsReturnedError(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error {
		if err := c.NoContent(http.StatusAccepted); err != nil {
			return err
		}
		return echo.NewHTTPError(http.StatusBadRequest, "too late")
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["status"] != float64(http.StatusAccepted) || entry["level"] != "INFO" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}

func TestAccessLoggerFallbackMetadataAnd404(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/missing/123", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["request_id"] == "" || rec.Header().Get(defaultRequestIDHeader) == "" {
		t.Fatalf("missing fallback request ID: entry=%#v headers=%#v", entry, rec.Header())
	}
	if entry["status"] != float64(http.StatusNotFound) || entry["path"] != "/missing/123" {
		t.Fatalf("unexpected 404 entry: %#v", entry)
	}
	if _, ok := entry["path_template"]; ok {
		t.Fatalf("unmatched route emitted path_template: %#v", entry)
	}
}

func TestAccessLoggerNilBaseLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
}

func TestAccessLoggerDoesNotMutateIncomingMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing"}
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("handler")
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if metadata.Logger != nil {
		t.Fatal("AccessLogger mutated incoming metadata")
	}
	line := strings.TrimSpace(strings.Split(buffer.String(), "\n")[1])
	if strings.Count(line, `"request_id"`) != 1 || strings.Count(line, `"correlation_id"`) != 1 {
		t.Fatalf("owned fields duplicated: %s", line)
	}
}

func TestAccessLoggerLogsAndRethrowsPanic(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	c := e.NewContext(
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil),
		httptest.NewRecorder(),
	)
	handler := AccessLogger(AccessLoggerConfig{Logger: logger})(func(*echo.Context) error {
		panic("boom")
	})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		if err := handler(c); err != nil {
			t.Fatalf("handler returned error before panic: %v", err)
		}
	}()
	if recovered != "boom" {
		t.Fatalf("recovered = %#v", recovered)
	}
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{"status": float64(http.StatusInternalServerError), "level": "ERROR"})
}

func TestAccessLoggerCloudPresets(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4efaaf4d1e8720b39541901950019ee5-00f067aa0ba902b7-01"
	tests := []struct {
		preset Preset
		want   map[string]any
	}{
		{
			PresetGCP,
			map[string]any{
				"severity":                             "INFO",
				"logging.googleapis.com/trace":         "4efaaf4d1e8720b39541901950019ee5",
				"logging.googleapis.com/trace_sampled": true,
			},
		},
		{PresetAWS, map[string]any{"level": "INFO", "xray_trace_id": "1-4efaaf4d-1e8720b39541901950019ee5"}},
		{
			PresetAzure,
			map[string]any{
				"level":              "INFO",
				"operation_Id":       "4efaaf4d1e8720b39541901950019ee5",
				"operation_ParentId": "00f067aa0ba902b7",
			},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: tt.preset, Writer: &buffer})
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{Logger: logger, Preset: tt.preset}))
			e.Use(AccessLogger(AccessLoggerConfig{
				Logger: logger,
				Preset: tt.preset,
				Now: fixedClock(
					time.Unix(0, 0),
					time.Unix(0, int64(1500*time.Millisecond)),
				),
			}))
			e.GET("/cloud", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com/cloud?x=1", nil)
			req.RemoteAddr = "203.0.113.9:4321"
			req.Header.Set("User-Agent", "cloud-test")
			req.Header.Set("Traceparent", traceparent)
			e.ServeHTTP(httptest.NewRecorder(), req)
			entry := decodeSingleLogLine(t, buffer.String())
			assertFields(t, entry, tt.want)
			if tt.preset == PresetGCP {
				httpRequest, ok := entry["httpRequest"].(map[string]any)
				if !ok {
					t.Fatalf("unexpected httpRequest: %#v", entry["httpRequest"])
				}
				assertFields(t, httpRequest, map[string]any{
					"requestMethod": "GET", "requestUrl": "https://example.com/cloud?x=1",
					"status": float64(http.StatusOK), "userAgent": "cloud-test",
					"remoteIp": "203.0.113.9", "latency": "1.5s",
				})
			} else if _, ok := entry["httpRequest"]; ok {
				t.Fatalf("non-GCP preset emitted httpRequest: %#v", entry)
			}
		})
	}
}

func TestAccessLoggerFallbackMetadataUsesConfiguredProvider(t *testing.T) {
	t.Parallel()
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	metadata := &requestMetadata{Logger: logger}
	ctx := contextWithRequestMetadata(t.Context(), metadata)
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Preset: PresetGCP,
	}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("handler")
		return c.NoContent(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.Header.Set("Traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	e.ServeHTTP(httptest.NewRecorder(), req)
	want := traceID
	for _, entry := range decodeLogLines(t, buffer.String()) {
		if entry["logging.googleapis.com/trace"] != want {
			t.Fatalf("GCP trace = %v, want %q; entry=%#v", entry["logging.googleapis.com/trace"], want, entry)
		}
	}
	if metadata.RequestID != "" || metadata.Logger != logger {
		t.Fatalf("incoming metadata was mutated: %#v", metadata)
	}
}

func TestProviderHeadersWithoutValidW3CAreIgnored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		preset       Preset
		header       string
		value        string
		traceparent  string
		rejectFields []string
	}{
		{
			name:   "gcp header only",
			preset: PresetGCP,
			header: "X-Cloud-Trace-Context",
			value:  "cccccccccccccccccccccccccccccccc/123;o=1",
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"logging.googleapis.com/trace",
				"logging.googleapis.com/trace_sampled",
			},
		},
		{
			name: "aws invalid w3c", preset: PresetAWS, header: "X-Amzn-Trace-Id",
			value: "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1", traceparent: "invalid",
			rejectFields: []string{"trace_id", "parent_id", "trace_flags", "trace_sampled", "xray_trace_id"},
		},
		{
			name:   "azure header only",
			preset: PresetAzure,
			header: "Request-Id",
			value:  "|4bf92f3577b34da6a3ce929d0e0e4736.00f067aa0ba902b7.",
			rejectFields: []string{
				"trace_id",
				"parent_id",
				"trace_flags",
				"trace_sampled",
				"operation_Id",
				"operation_ParentId",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: tt.preset, Writer: &buffer})
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{Logger: logger, Preset: tt.preset}))
			e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, Preset: tt.preset}))
			e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header.Set(defaultRequestIDHeader, "req-provider")
			req.Header.Set(tt.header, tt.value)
			if tt.traceparent != "" {
				req.Header.Set("Traceparent", tt.traceparent)
			}
			e.ServeHTTP(httptest.NewRecorder(), req)
			entry := decodeSingleLogLine(t, buffer.String())
			if entry["correlation_id"] != "req-provider" {
				t.Fatalf("correlation = %#v", entry["correlation_id"])
			}
			for _, field := range tt.rejectFields {
				if _, ok := entry[field]; ok {
					t.Fatalf("provider header produced %s: %#v", field, entry)
				}
			}
		})
	}
}

func TestProviderTraceFieldsOnHandlerAndAccessLines(t *testing.T) {
	t.Parallel()
	const traceID = "4efaaf4d1e8720b39541901950019ee5"
	const parentID = "00f067aa0ba902b7"
	tests := []struct {
		preset Preset
		flags  string
		want   map[string]any
		reject []string
	}{
		{
			PresetGCP,
			"00",
			map[string]any{"logging.googleapis.com/trace": traceID, "logging.googleapis.com/trace_sampled": false},
			[]string{"logging.googleapis.com/spanId", "xray_trace_id", "operation_Id"},
		},
		{
			PresetAWS,
			"01",
			map[string]any{"xray_trace_id": "1-4efaaf4d-1e8720b39541901950019ee5"},
			[]string{"logging.googleapis.com/trace", "operation_Id", "xray_parent_id"},
		},
		{
			PresetAzure,
			"01",
			map[string]any{"operation_Id": traceID, "operation_ParentId": parentID},
			[]string{"logging.googleapis.com/trace", "xray_trace_id"},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: tt.preset, Writer: &buffer})
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{Logger: logger, Preset: tt.preset}))
			e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, Preset: tt.preset}))
			e.GET("/", func(c *echo.Context) error {
				Logger(c.Request().Context()).Info("handler cloud log")
				return c.NoContent(http.StatusOK)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header.Set("Traceparent", "00-"+traceID+"-"+parentID+"-"+tt.flags)
			req.Header.Set("Request-Id", "|aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbb.")
			req.Header.Set(
				"X-Amzn-Trace-Id",
				"Root=1-aaaaaaaa-bbbbbbbbbbbbbbbbbbbbbbbb;Parent=1111111111111111;Sampled=0",
			)
			req.Header.Set("X-Cloud-Trace-Context", "cccccccccccccccccccccccccccccccc/123;o=1")
			e.ServeHTTP(httptest.NewRecorder(), req)
			lines := decodeLogLines(t, buffer.String())
			if len(lines) != 2 {
				t.Fatalf("lines = %#v", lines)
			}
			for _, entry := range lines {
				assertFields(t, entry, map[string]any{
					"trace_id":      traceID,
					"parent_id":     parentID,
					"trace_flags":   tt.flags,
					"trace_sampled": tt.flags == "01",
				})
				assertFields(t, entry, tt.want)
				for _, key := range tt.reject {
					if _, ok := entry[key]; ok {
						t.Fatalf("unexpected %s: %#v", key, entry)
					}
				}
			}
		})
	}
}

func TestAccessLoggerFiltersReservedFields(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger: logger,
		ExtraFields: func(*echo.Context) []zap.Field {
			return []zap.Field{
				zap.Int("status", 999),
				zap.String("error", "forged"),
				zap.String("tenant_id", "tenant-1"),
			}
		},
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	line := strings.TrimSpace(buffer.String())
	if strings.Count(line, `"status"`) != 1 || strings.Contains(line, "forged") {
		t.Fatalf("reserved field override leaked: %s", line)
	}
	entry := decodeSingleLogLine(t, line)
	if entry["tenant_id"] != "tenant-1" {
		t.Fatalf("custom field missing: %#v", entry)
	}
}

func TestAccessLoggerFiltersProviderReservedFields(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4efaaf4d1e8720b39541901950019ee5-00f067aa0ba902b7-01"
	providerKeys := []string{
		"logging.googleapis.com/trace", "logging.googleapis.com/trace_sampled",
		"logging.googleapis.com/spanId", "xray_trace_id", "operation_Id", "operation_ParentId",
	}
	for _, preset := range []Preset{PresetDefault, PresetGCP, PresetAWS, PresetAzure} {
		t.Run(string(preset), func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: preset, Writer: &buffer})
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{Logger: logger, Preset: preset}))
			e.Use(AccessLogger(AccessLoggerConfig{
				Logger: logger,
				Preset: preset,
				ExtraFields: func(*echo.Context) []zap.Field {
					return []zap.Field{
						zap.String("logging.googleapis.com/trace", "bad-gcp-trace"),
						zap.Bool("logging.googleapis.com/trace_sampled", false),
						zap.String("logging.googleapis.com/spanId", "bad-gcp-span"),
						zap.String("xray_trace_id", "bad-xray-trace"),
						zap.String("operation_Id", "bad-azure-operation"),
						zap.String("operation_ParentId", "bad-azure-parent"),
						zap.String("tenant_id", "tenant-1"),
					}
				},
			}))
			e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header.Set("Traceparent", traceparent)
			e.ServeHTTP(httptest.NewRecorder(), req)
			line := strings.TrimSpace(buffer.String())
			if strings.Contains(line, "bad-") {
				t.Fatalf("provider override leaked: %s", line)
			}
			for _, key := range providerKeys {
				if count := strings.Count(line, `"`+key+`"`); count > 1 {
					t.Fatalf("%s count = %d: %s", key, count, line)
				}
			}
			entry := decodeSingleLogLine(t, line)
			if entry["tenant_id"] != "tenant-1" {
				t.Fatalf("custom field missing: %#v", entry)
			}
		})
	}
}

func TestAccessLoggerCustomLevelAndNegativeDuration(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	start := time.Date(2026, 7, 12, 12, 0, 1, 0, time.UTC)
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger:      logger,
		Now:         fixedClock(start, start.Add(-time.Second)),
		StatusLevel: func(int) zapcore.Level { return zap.DebugLevel },
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusTeapot) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	// Info-level logger suppresses the configured debug access log without panicking.
	if buffer.Len() != 0 {
		t.Fatalf("unexpected disabled log: %s", buffer.String())
	}

	debug := newTestLogger(t, LoggerConfig{Writer: &buffer, Level: zap.DebugLevel})
	e = echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger:      debug,
		Now:         fixedClock(start, start.Add(-time.Second)),
		StatusLevel: func(int) zapcore.Level { return zap.DebugLevel },
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusTeapot) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{"level": "DEBUG", "duration_ms": float64(0)})
}

func TestAccessLogHelpers(t *testing.T) {
	t.Parallel()
	durationTests := map[time.Duration]string{
		-time.Millisecond:             "0s",
		0:                             "0s",
		3 * time.Second:               "3s",
		1500 * time.Millisecond:       "1.5s",
		time.Second + time.Nanosecond: "1.000000001s",
	}
	for input, want := range durationTests {
		if got := formatProtoDuration(input); got != want {
			t.Errorf("formatProtoDuration(%s) = %q, want %q", input, got, want)
		}
	}
	if got := xrayTraceIDFromW3C("short"); got != "" {
		t.Fatalf("short X-Ray trace = %q", got)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.URL.Path = ""
	req.URL.Opaque = "/opaque"
	if requestPath(req) != "/opaque" {
		t.Fatalf("opaque request path = %q", requestPath(req))
	}
	req.URL.Opaque = ""
	req.Host = ""
	e := echo.New()
	c := e.NewContext(req, httptest.NewRecorder())
	if requestPath(req) != "/" || requestURL(c) != "/" {
		t.Fatalf("empty request path/url = %q/%q", requestPath(req), requestURL(c))
	}
	proxied := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/secure?x=1", nil)
	proxied.URL.Scheme = ""
	proxied.URL.Host = ""
	proxied.Header.Set(echo.HeaderXForwardedProto, "https")
	proxyContext := e.NewContext(proxied, httptest.NewRecorder())
	if got := requestURL(proxyContext); got != "https://example.com/secure?x=1" {
		t.Fatalf("proxied request URL = %q", got)
	}
}

func fixedClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func assertFields(t *testing.T, entry, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if entry[key] != value {
			t.Fatalf("%s = %#v, want %#v; entry=%#v", key, entry[key], value, entry)
		}
	}
}

func decodeLogLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, decodeSingleLogLine(t, line))
	}
	return entries
}

func newTestLogger(t *testing.T, config LoggerConfig) *zap.Logger {
	t.Helper()
	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("NewLogger returned error: %v", err)
	}
	return logger
}
