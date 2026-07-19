package obs

import (
	"bytes"
	"context"
	"errors"
	"io"
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

type panickingWriter struct {
	value any
}

func (w panickingWriter) Write([]byte) (int, error) {
	panic(w.value)
}

func (e *testStatusError) Error() string {
	return http.StatusText(e.status)
}

func (e *testStatusError) StatusCode() int {
	return e.status
}

func TestAccessLoggerEmitsRouteTemplateAndRequestMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(
		RequestContext(RequestContextConfig{Logger: logger}),
		AccessLogger(AccessLoggerConfig{
			Logger:           logger,
			CapturePath:      true,
			CapturePeerIP:    true,
			CaptureUserAgent: true,
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
			if got := RequestID(c.Request().Context()); got != "req-route" {
				t.Fatalf("request ID = %q, want req-route", got)
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
		"path": "/users/123", "path_template": "/users/{id}", "operation_id": "get-user",
		"status": float64(http.StatusCreated), "duration_ms": float64(250),
		"peer_ip": "203.0.113.9", "user_agent": "test-agent", "tenant_id": "tenant-1",
	})
}

func TestRequestIDScenariosReplaceAmbiguousValuesBeforeTerminalOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rawValues []string
		generated string
		duration  time.Duration
		forbidden []string
	}{
		{
			name: "duplicate", rawValues: []string{"caller-one", "caller-two"},
			generated: "duplicate-replaced", duration: 2 * time.Millisecond,
			forbidden: []string{"caller-one", "caller-two"},
		},
		{
			name: "invalid", rawValues: []string{"bad value"},
			generated: "generated-safe", duration: time.Millisecond,
			forbidden: []string{"bad value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Writer: &output})
			e := echo.New()
			e.Use(
				RequestContext(RequestContextConfig{
					Logger:       logger,
					NewRequestID: func() string { return tt.generated },
				}),
				AccessLogger(AccessLoggerConfig{
					Logger: logger,
					Now: fixedClock(
						time.Unix(1, 0),
						time.Unix(1, int64(tt.duration)),
					),
				}),
			)
			e.GET("/request-id", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/request-id", nil)
			for _, value := range tt.rawValues {
				request.Header.Add(defaultRequestIDHeader, value)
			}
			response := httptest.NewRecorder()

			e.ServeHTTP(response, request)

			if got := response.Header().Get(defaultRequestIDHeader); got != tt.generated {
				t.Fatalf("response request ID = %q, want %q", got, tt.generated)
			}
			record := decodeSingleLogLine(t, output.String())
			assertFields(t, record, map[string]any{
				"message": "request completed", "request_id": tt.generated,
				"correlation_id": tt.generated, "method": http.MethodGet,
				"duration_ms": float64(tt.duration.Microseconds()) / 1000,
				"status":      float64(http.StatusNoContent), "path_template": "/request-id",
			})
			for _, forbidden := range tt.forbidden {
				if strings.Contains(output.String(), forbidden) {
					t.Fatalf("terminal output contains rejected request ID %q: %s", forbidden, output.String())
				}
			}
		})
	}
}

func TestAccessLoggerPeerIPUsesDirectSocketAndIgnoresEchoIPExtractor(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.IPExtractor = func(*http.Request) string { return "198.51.100.42" }
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, CapturePeerIP: true}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:4321"
	e.ServeHTTP(httptest.NewRecorder(), req)
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["peer_ip"] != "203.0.113.9" {
		t.Fatalf("peer_ip = %v, want direct socket peer", entry["peer_ip"])
	}
	if _, ok := entry["remote_ip"]; ok {
		t.Fatalf("legacy remote_ip unexpectedly emitted: %#v", entry)
	}
}

func TestAccessLoggerPrivacyFieldsAreDisabledByDefault(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	e := echo.New()
	e.IPExtractor = func(*http.Request) string { return "198.51.100.42" }
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, Preset: PresetGCP}))
	e.GET("/private", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/private?secret=yes", nil)
	req.RemoteAddr = "203.0.113.9:4321"
	req.Header.Set("User-Agent", "privacy-canary")
	e.ServeHTTP(httptest.NewRecorder(), req)

	entry := decodeSingleLogLine(t, buffer.String())
	for _, key := range []string{"path", "peer_ip", "remote_ip", "user_agent"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("privacy field %q enabled by default: %#v", key, entry)
		}
	}
	httpRequest, ok := entry["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
	}
	assertFields(t, httpRequest, map[string]any{
		"requestMethod": "GET", "status": float64(http.StatusOK),
	})
	for _, key := range []string{"requestUrl", "remoteIp", "userAgent"} {
		if _, ok := httpRequest[key]; ok {
			t.Fatalf("GCP privacy field %q enabled by default: %#v", key, httpRequest)
		}
	}
	if len(httpRequest) != 3 {
		t.Fatalf("httpRequest = %#v, want method, status, and latency only", httpRequest)
	}
}

func TestAccessLoggerPrivacyCaptureOptionsAreIndependent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		config    AccessLoggerConfig
		portable  string
		provider  string
		wantValue string
	}{
		{"path", AccessLoggerConfig{CapturePath: true}, "path", "requestUrl", "/private"},
		{"peer IP", AccessLoggerConfig{CapturePeerIP: true}, "peer_ip", "remoteIp", "203.0.113.9"},
		{"user agent", AccessLoggerConfig{CaptureUserAgent: true}, "user_agent", "userAgent", "privacy-canary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: PresetGCP, Writer: &buffer})
			e := echo.New()
			tt.config.Logger = logger
			tt.config.Preset = PresetGCP
			e.Use(AccessLogger(tt.config))
			e.GET("/private", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/private?secret=yes", nil)
			req.RemoteAddr = "203.0.113.9:4321"
			req.Header.Set("User-Agent", "privacy-canary")
			e.ServeHTTP(httptest.NewRecorder(), req)

			entry := decodeSingleLogLine(t, buffer.String())
			assertFields(t, entry, map[string]any{tt.portable: tt.wantValue})
			for _, key := range []string{"path", "peer_ip", "user_agent"} {
				if key != tt.portable {
					if _, ok := entry[key]; ok {
						t.Fatalf("capture option enabled %q: %#v", key, entry)
					}
				}
			}
			httpRequest, ok := entry["httpRequest"].(map[string]any)
			if !ok {
				t.Fatalf("httpRequest missing or wrong type: %#v", entry["httpRequest"])
			}
			assertFields(t, httpRequest, map[string]any{tt.provider: tt.wantValue})
			for _, key := range []string{"requestUrl", "remoteIp", "userAgent"} {
				if key != tt.provider {
					if _, ok := httpRequest[key]; ok {
						t.Fatalf("capture option enabled GCP field %q: %#v", key, httpRequest)
					}
				}
			}
		})
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
	if lines[0]["message"] != "handler log" || lines[1]["message"] != "request completed" {
		t.Fatalf("unexpected log sequence: %#v", lines)
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

func TestDefaultStatusLevelUsesExactHTTPClassBoundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		status int
		want   zapcore.Level
	}{
		{name: "last 3xx", status: 399, want: zapcore.InfoLevel},
		{name: "first 4xx", status: http.StatusBadRequest, want: zapcore.WarnLevel},
		{name: "last 4xx", status: 499, want: zapcore.WarnLevel},
		{name: "first 5xx", status: http.StatusInternalServerError, want: zapcore.ErrorLevel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultStatusLevel(tt.status); got != tt.want {
				t.Fatalf("DefaultStatusLevel(%d) = %s, want %s", tt.status, got, tt.want)
			}
		})
	}
}

func TestAccessLoggerContainsTelemetryCallbackPanics(t *testing.T) {
	t.Run("status mapper uses default", func(t *testing.T) {
		var buffer bytes.Buffer
		logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
		e := echo.New()
		e.Use(AccessLogger(AccessLoggerConfig{
			Logger: logger,
			StatusLevel: func(int) zapcore.Level {
				panic("status mapper failed")
			},
		}))
		e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusNotFound) })
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("response status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		entry := decodeSingleLogLine(t, buffer.String())
		assertFields(t, entry, map[string]any{"level": "WARN", "status": float64(http.StatusNotFound)})
	})

	t.Run("clock and enrichment use safe values", func(t *testing.T) {
		var buffer bytes.Buffer
		logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
		clockCalls := 0
		e := echo.New()
		e.Use(AccessLogger(AccessLoggerConfig{
			Logger: logger,
			Now: func() time.Time {
				clockCalls++
				if clockCalls == 1 {
					return time.Unix(0, 0)
				}
				panic("clock failed")
			},
			ExtraFields: func(*echo.Context) []zap.Field {
				panic("enrichment failed")
			},
		}))
		e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusAccepted) })
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

		if rec.Code != http.StatusAccepted {
			t.Fatalf("response status = %d, want %d", rec.Code, http.StatusAccepted)
		}
		entry := decodeSingleLogLine(t, buffer.String())
		assertFields(t, entry, map[string]any{"duration_ms": float64(0), "status": float64(http.StatusAccepted)})
		if _, ok := entry["tenant_id"]; ok {
			t.Fatalf("failed enrichment leaked fields: %#v", entry)
		}
	})
}

func TestSafeStatusLevelRejectsTerminalAndUnknownLevels(t *testing.T) {
	t.Parallel()

	for _, level := range []zapcore.Level{
		zapcore.DPanicLevel,
		zapcore.PanicLevel,
		zapcore.FatalLevel,
		zapcore.Level(99),
	} {
		t.Run(level.String(), func(t *testing.T) {
			t.Parallel()
			got := safeStatusLevel(func(int) zapcore.Level { return level }, http.StatusNotFound)
			if got != zapcore.WarnLevel {
				t.Fatalf("safeStatusLevel returned %s, want WARN fallback", got)
			}
		})
	}
}

func TestAccessLoggerContainsWriterPanicWithoutChangingResponse(t *testing.T) {
	logger := newTestLogger(t, LoggerConfig{Writer: panickingWriter{value: "writer failed"}})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

func TestAccessLoggerDoesNotClaimStatusesProducedAfterReturnedHandlerErrors(t *testing.T) {
	t.Parallel()
	httpErr := echo.NewHTTPError(http.StatusBadRequest, "bad input")
	plainErr := errors.New("database unavailable")
	statusErr := &testStatusError{status: http.StatusUnprocessableEntity}
	tests := []struct {
		name       string
		handler    echo.HandlerFunc
		wantCode   int
		wantLevel  string
		wantError  string
		wantStatus bool
	}{
		{
			"http error",
			func(*echo.Context) error { return httpErr },
			http.StatusBadRequest,
			"ERROR",
			httpErr.Error(),
			false,
		},
		{
			"plain error",
			func(*echo.Context) error { return plainErr },
			http.StatusInternalServerError,
			"ERROR",
			plainErr.Error(),
			false,
		},
		{
			"custom status error",
			func(*echo.Context) error { return statusErr },
			http.StatusUnprocessableEntity,
			"ERROR",
			statusErr.Error(),
			false,
		},
		{
			"success",
			func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) },
			http.StatusNoContent,
			"INFO",
			"",
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
			e := echo.New()
			e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, CaptureError: true}))
			e.GET("/test", tt.handler)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("wire status = %d, want %d", rec.Code, tt.wantCode)
			}
			entry := decodeSingleLogLine(t, buffer.String())
			assertFields(t, entry, map[string]any{"level": tt.wantLevel})
			status, hasStatus := entry["status"]
			if hasStatus != tt.wantStatus {
				t.Fatalf("status presence = %t, want %t: %#v", hasStatus, tt.wantStatus, entry)
			}
			if tt.wantStatus && status != float64(tt.wantCode) {
				t.Fatalf("status = %#v, want %d: %#v", status, tt.wantCode, entry)
			}
			gotError, hasError := entry["error"]
			if tt.wantError == "" {
				if hasError {
					t.Fatalf("successful request emitted error field: %#v", entry)
				}
			} else if !hasError || gotError != tt.wantError {
				t.Fatalf("error = %#v, want %q: %#v", gotError, tt.wantError, entry)
			}
			if tt.wantError == "" {
				if _, ok := entry["terminal_reason"]; ok {
					t.Fatalf("successful request emitted terminal reason: %#v", entry)
				}
			} else if entry["terminal_reason"] != "service_error" {
				t.Fatalf("terminal_reason = %#v, want service_error: %#v", entry["terminal_reason"], entry)
			}
		})
	}
}

func TestAccessLoggerCommittedStatusWinsReturnedError(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, CaptureError: true}))
	lateErr := echo.NewHTTPError(http.StatusBadRequest, "too late")
	e.GET("/", func(c *echo.Context) error {
		if err := c.NoContent(http.StatusAccepted); err != nil {
			return err
		}
		return lateErr
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	if rec.Code != http.StatusAccepted || entry["status"] != float64(http.StatusAccepted) ||
		entry["level"] != "ERROR" || entry["terminal_reason"] != "service_error" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	if got := entry["error"]; got != lateErr.Error() {
		t.Fatalf("committed response error = %#v, want %q: %#v", got, lateErr.Error(), entry)
	}
}

func TestAccessLoggerNormalReturnWithoutCommittedResponseOmitsStatus(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	statusLevelCalls := 0
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger: logger,
		StatusLevel: func(int) zapcore.Level {
			statusLevelCalls++
			return zap.DebugLevel
		},
	}))
	e.GET("/", func(*echo.Context) error { return nil })
	e.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
	)
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["level"] != "INFO" || statusLevelCalls != 0 {
		t.Fatalf("normal uncommitted outcome = %#v, status-level calls = %d", entry, statusLevelCalls)
	}
	if _, ok := entry["status"]; ok {
		t.Fatalf("normal uncommitted outcome inferred status: %#v", entry)
	}
	if _, ok := entry["terminal_reason"]; ok {
		t.Fatalf("normal uncommitted outcome emitted terminal reason: %#v", entry)
	}
}

func TestAccessLoggerGCPServiceErrorOmitsUnobservedStatusAndForcesError(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	statusLevelCalls := 0
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger:       logger,
		Preset:       PresetGCP,
		CaptureError: true,
		StatusLevel: func(int) zapcore.Level {
			statusLevelCalls++
			return zap.DebugLevel
		},
	}))
	e.GET("/", func(*echo.Context) error { return errors.New("service failed") })
	e.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
	)
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"severity": "ERROR", "terminal_reason": "service_error", "error": "service failed",
	})
	if statusLevelCalls != 0 {
		t.Fatalf("abnormal outcome invoked status-level callback %d times", statusLevelCalls)
	}
	if _, ok := entry["status"]; ok {
		t.Fatalf("service error inferred top-level status: %#v", entry)
	}
	httpRequest, ok := entry["httpRequest"].(map[string]any)
	if !ok {
		t.Fatalf("httpRequest = %#v, want object", entry["httpRequest"])
	}
	if _, ok := httpRequest["status"]; ok {
		t.Fatalf("service error inferred GCP status: %#v", httpRequest)
	}
}

func TestAccessLoggerOmitsReturnedErrorDetailsByDefault(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		preset Preset
	}{
		{name: "default", preset: PresetDefault},
		{name: "gcp", preset: PresetGCP},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger := newTestLogger(t, LoggerConfig{Preset: test.preset, Writer: &buffer})
			e := echo.New()
			e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, Preset: test.preset}))
			e.GET("/", func(*echo.Context) error { return errors.New("ERROR_SECRET") })
			e.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
			)
			entry := decodeSingleLogLine(t, buffer.String())
			if _, ok := entry["error"]; ok || strings.Contains(buffer.String(), "ERROR_SECRET") {
				t.Fatalf("default access record leaked rich error details: %s", buffer.String())
			}
			if entry["terminal_reason"] != "service_error" {
				t.Fatalf("terminal reason = %#v, want service_error", entry["terminal_reason"])
			}
		})
	}
}

func TestAccessLoggerFallbackMetadataAnd404(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, CapturePath: true}))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/missing/123", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	loggedID, ok := entry["request_id"].(string)
	headerID := rec.Header().Get(defaultRequestIDHeader)
	if !ok || loggedID == "" || headerID == "" || loggedID != headerID {
		t.Fatalf("missing fallback request ID: entry=%#v headers=%#v", entry, rec.Header())
	}
	if entry["correlation_id"] != loggedID {
		t.Fatalf("fallback correlation ID does not match request ID: %#v", entry)
	}
	if _, ok := entry["status"]; ok || entry["terminal_reason"] != "service_error" ||
		entry["level"] != "ERROR" || entry["path"] != "/missing/123" {
		t.Fatalf("unexpected 404 entry: %#v", entry)
	}
	if _, ok := entry["path_template"]; ok {
		t.Fatalf("unmatched route emitted path_template: %#v", entry)
	}
	if _, ok := entry["operation_id"]; ok {
		t.Fatalf("unmatched route emitted operation_id: %#v", entry)
	}
}

func TestAccessLoggerUnnamedRouteKeepsEscapedPathWithoutOperationID(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger, CapturePath: true}))
	e.GET("/files/*", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	for _, target := range []string{"/files/a%2Fb?download=true", "/files/tenant-b/two"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 2 || entries[0]["path"] != "/files/a%2Fb" || entries[1]["path"] != "/files/tenant-b/two" {
		t.Fatalf("unexpected concrete route paths: %#v", entries)
	}
	for _, entry := range entries {
		if entry["path_template"] != "/files/{*path}" {
			t.Fatalf("catch-all template changed with request data: %#v", entries)
		}
		if _, ok := entry["operation_id"]; ok {
			t.Fatalf("unnamed route emitted Echo's synthetic operation ID: %#v", entry)
		}
	}
}

func TestAccessLoggerRepresentativeRouteIdentityHasStableCardinality(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	for _, route := range []echo.Route{
		{Method: http.MethodGet, Path: "/items/:item_id", Name: "get_item", Handler: func(c *echo.Context) error { return c.NoContent(http.StatusOK) }},
		{Method: http.MethodGet, Path: "/files/*", Name: "get_file", Handler: func(c *echo.Context) error { return c.NoContent(http.StatusOK) }},
	} {
		if _, err := e.AddRoute(route); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"/items/tenant-a", "/items/tenant-b", "/files/tenant-a/one", "/files/tenant-b/two"} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	}
	entries := decodeLogLines(t, buffer.String())
	if len(entries) != 4 {
		t.Fatalf("log line count = %d, want 4", len(entries))
	}
	for _, entry := range entries[:2] {
		assertFields(t, entry, map[string]any{"path_template": "/items/{item_id}", "operation_id": "get_item"})
	}
	for _, entry := range entries[2:] {
		assertFields(t, entry, map[string]any{"path_template": "/files/{*path}", "operation_id": "get_file"})
	}
}

func TestAccessMetadataStringsRejectEmptyDuplicateAndControlValues(t *testing.T) {
	t.Parallel()

	if _, ok := singleValidHeaderValue([]string{"agent/1"}); !ok {
		t.Fatal("one valid User-Agent was rejected")
	}
	for _, values := range [][]string{nil, {""}, {"agent/1", "agent/1"}, {"agent/1\nforged"}} {
		if value, ok := singleValidHeaderValue(values); ok || value != "" {
			t.Fatalf("singleValidHeaderValue(%q) = %q, %v; want empty, false", values, value, ok)
		}
	}
	if validMetadataString("get_item\nforged") {
		t.Fatal("operation ID containing a control character was accepted")
	}
	for _, value := range []string{"\x1f", "\x7f"} {
		if validMetadataString(value) {
			t.Fatalf("metadata control boundary %q was accepted", value)
		}
	}
	for _, value := range []string{" ", "~"} {
		if !validMetadataString(value) {
			t.Fatalf("printable metadata boundary %q was rejected", value)
		}
	}
}

func TestAccessLogger405OmitsSyntheticRouteMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/users/:id", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/users/123", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected 405 response: code=%d entry=%#v", rec.Code, entry)
	}
	if _, ok := entry["status"]; ok {
		t.Fatalf("unobserved centralized 405 status was logged: %#v", entry)
	}
	if entry["terminal_reason"] != "service_error" || entry["level"] != "ERROR" {
		t.Fatalf("unexpected 405 terminal outcome: %#v", entry)
	}
	if entry["path_template"] != "/users/{id}" {
		t.Fatalf("method-not-allowed route lost matched path_template: %#v", entry)
	}
	if _, ok := entry["operation_id"]; ok {
		t.Fatalf("method-not-allowed route emitted operation_id: %#v", entry)
	}
}

func TestCanonicalRouteTemplateCurrentEchoForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		native string
		want   string
		ok     bool
	}{
		{native: "/health", want: "/health", ok: true},
		{native: "/items/:item_id", want: "/items/{item_id}", ok: true},
		{native: "/files/*", want: "/files/{*path}", ok: true},
		{native: "*", want: "/{*path}", ok: true},
		{native: "/items/:item_id?"},
		{native: "/items/:item_id.:format"},
		{native: "/files/*/suffix"},
	} {
		t.Run(test.native, func(t *testing.T) {
			t.Parallel()
			got, ok := canonicalRouteTemplate(test.native)
			if got != test.want || ok != test.ok {
				t.Fatalf(
					"canonicalRouteTemplate(%q) = (%q, %v), want (%q, %v)",
					test.native, got, ok, test.want, test.ok,
				)
			}
		})
	}
	for _, name := range []string{"A", "Z", "a", "z", "_", "a0", "a9", strings.Repeat("a", 64)} {
		t.Run("valid-name-"+name, func(t *testing.T) {
			t.Parallel()
			native := "/items/:" + name
			want := "/items/{" + name + "}"
			if got, ok := canonicalRouteTemplate(native); !ok || got != want {
				t.Fatalf("canonicalRouteTemplate(%q) = (%q, %v), want (%q, true)", native, got, ok, want)
			}
		})
	}
	for _, name := range []string{"", "0a", "9a", "@a", "[a", "`a", "{a", "a.", "a:", strings.Repeat("a", 65)} {
		t.Run("invalid-name-"+name, func(t *testing.T) {
			t.Parallel()
			if got, ok := canonicalRouteTemplate("/items/:" + name); ok || got != "" {
				t.Fatalf("invalid parameter name %q produced (%q, %v)", name, got, ok)
			}
		})
	}
}

func TestAccessLoggerWithoutBaseLoggerStillInstallsRequestContext(t *testing.T) {
	t.Parallel()
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{}))
	var requestID string
	e.GET("/", func(c *echo.Context) error {
		requestID = RequestID(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if requestID == "" || rec.Header().Get(defaultRequestIDHeader) != requestID || rec.Code != http.StatusNoContent {
		t.Fatalf("fallback context/status = %q/%q/%d", requestID, rec.Header().Get(defaultRequestIDHeader), rec.Code)
	}
}

func TestAccessLoggerDoesNotMutateIncomingMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing"}
	original := *metadata
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("handler")
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if *metadata != original {
		t.Fatalf("AccessLogger mutated incoming metadata: got=%#v want=%#v", metadata, original)
	}
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want handler and access lines: %q", len(lines), buffer.String())
	}
	handlerEntry := decodeSingleLogLine(t, lines[0])
	accessEntry := decodeSingleLogLine(t, lines[1])
	assertFields(t, handlerEntry, map[string]any{
		"message": "handler", "request_id": "existing", "correlation_id": "existing",
	})
	assertFields(t, accessEntry, map[string]any{
		"message": "request completed", "request_id": "existing", "correlation_id": "existing",
	})
	line := lines[1]
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
	assertFields(t, entry, map[string]any{
		"message": "request completed", "terminal_reason": "panic", "level": "ERROR",
	})
	if _, ok := entry["status"]; ok {
		t.Fatalf("uncommitted panic emitted inferred status: %#v", entry)
	}
}

func TestAccessLoggerPreservesCommittedStatusAndRethrowsPanic(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil),
		recorder,
	)
	handler := AccessLogger(AccessLoggerConfig{Logger: logger})(func(c *echo.Context) error {
		if err := c.NoContent(http.StatusAccepted); err != nil {
			return err
		}
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
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("wire status = %d, want %d", recorder.Code, http.StatusAccepted)
	}
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"message": "request completed", "status": float64(http.StatusAccepted),
		"terminal_reason": "panic", "level": "ERROR",
	})
}

func TestAccessLoggerPreservesHandlerPanicWhenAccessLoggingPanics(t *testing.T) {
	t.Parallel()
	type panicMarker struct {
		name string
	}
	for _, tt := range []struct {
		name  string
		stage string
	}{
		{name: "enrichment callback", stage: "enrichment"},
		{name: "log writer", stage: "writer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handlerPanic := &panicMarker{name: "handler panic"}
			loggingPanic := &panicMarker{name: "access logging panic"}
			var writer io.Writer = &bytes.Buffer{}
			if tt.stage == "writer" {
				writer = panickingWriter{value: loggingPanic}
			}
			logger := newTestLogger(t, LoggerConfig{Writer: writer})
			config := AccessLoggerConfig{Logger: logger}
			switch tt.stage {
			case "enrichment":
				config.ExtraFields = func(*echo.Context) []zap.Field {
					panic(loggingPanic)
				}
			}
			e := echo.New()
			c := e.NewContext(
				httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/panic", nil),
				httptest.NewRecorder(),
			)
			handler := AccessLogger(config)(func(*echo.Context) error {
				panic(handlerPanic)
			})
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				if err := handler(c); err != nil {
					t.Fatalf("handler returned before panic: %v", err)
				}
			}()
			if recovered != handlerPanic {
				t.Fatalf("recovered panic = %#v, want original %#v", recovered, handlerPanic)
			}
		})
	}
}

func TestContainAccessLogReportsCompletion(t *testing.T) {
	t.Parallel()

	if !containAccessLog(func() {}) {
		t.Fatal("containAccessLog reported failure for a completed write")
	}
	if containAccessLog(func() { panic("writer failed") }) {
		t.Fatal("containAccessLog reported completion for a panicking write")
	}
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
				Logger:           logger,
				Preset:           tt.preset,
				CapturePath:      true,
				CapturePeerIP:    true,
				CaptureUserAgent: true,
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
					"requestMethod": "GET", "requestUrl": "/cloud",
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
	lines := decodeLogLines(t, buffer.String())
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want handler and access lines: %#v", len(lines), lines)
	}
	if lines[0]["message"] != "handler" || lines[1]["message"] != "request completed" {
		t.Fatalf("unexpected log sequence: %#v", lines)
	}
	for _, entry := range lines {
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
	reservedKeys := []string{
		"timestamp", "level", "severity", "logger", "message", "error",
		"request_id", "correlation_id", "trace_id", "parent_id", "trace_flags", "trace_sampled",
		"method", "path", "path_template", "operation_id", "status", "duration_ms",
		"terminal_reason", "peer_ip", "remote_ip", "user_agent", "httpRequest",
	}
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger: logger,
		ExtraFields: func(*echo.Context) []zap.Field {
			fields := make([]zap.Field, 0, len(reservedKeys)+2)
			for _, key := range reservedKeys {
				fields = append(fields, zap.String(key, "forged-"+key))
			}
			return append(
				fields,
				zap.String("tenant_id", "tenant-1"),
				zap.String("tenant_id", "tenant-2"),
			)
		},
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusOK) })
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	line := strings.TrimSpace(buffer.String())
	if strings.Count(line, `"status"`) != 1 || strings.Contains(line, "forged-") {
		t.Fatalf("reserved field override leaked: %s", line)
	}
	entry := decodeSingleLogLine(t, line)
	if entry["tenant_id"] != "tenant-1" {
		t.Fatalf("custom field missing: %#v", entry)
	}
	if count := strings.Count(line, `"tenant_id"`); count != 1 {
		t.Fatalf("tenant_id count = %d, want 1: %s", count, line)
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

func TestAccessLoggerSkipsEnrichmentWhenLevelDisabled(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	extraCalls := 0
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger:      logger,
		StatusLevel: func(int) zapcore.Level { return zap.DebugLevel },
		ExtraFields: func(*echo.Context) []zap.Field {
			extraCalls++
			return []zap.Field{zap.String("tenant_id", "tenant-1")}
		},
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("wire status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if buffer.Len() != 0 {
		t.Fatalf("unexpected disabled log: %s", buffer.String())
	}
	if extraCalls != 0 {
		t.Fatalf("ExtraFields called %d times for a disabled log level", extraCalls)
	}
}

func TestAccessLoggerUsesCustomLevelAndClampsNegativeDuration(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	debug := newTestLogger(t, LoggerConfig{Writer: &buffer, Level: zap.DebugLevel})
	start := time.Date(2026, 7, 12, 12, 0, 1, 0, time.UTC)
	extraCalls := 0
	e := echo.New()
	e.Use(AccessLogger(AccessLoggerConfig{
		Logger:      debug,
		Now:         fixedClock(start, start.Add(-time.Second)),
		StatusLevel: func(int) zapcore.Level { return zap.DebugLevel },
		ExtraFields: func(*echo.Context) []zap.Field {
			extraCalls++
			return []zap.Field{zap.String("tenant_id", "tenant-1")}
		},
	}))
	e.GET("/", func(c *echo.Context) error { return c.NoContent(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"message": "request completed", "level": "DEBUG", "status": float64(http.StatusTeapot),
		"duration_ms": float64(0), "tenant_id": "tenant-1",
	})
	if rec.Code != http.StatusTeapot {
		t.Fatalf("wire status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if extraCalls != 1 {
		t.Fatalf("ExtraFields calls = %d, want one enabled access log", extraCalls)
	}
}

func TestFormatProtoDurationClampsAndFormatsFractionalSeconds(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		input time.Duration
		want  string
	}{
		{name: "negative clamps to zero", input: -time.Millisecond, want: "0s"},
		{name: "zero", want: "0s"},
		{name: "whole seconds", input: 3 * time.Second, want: "3s"},
		{name: "trims fractional zeroes", input: 1500 * time.Millisecond, want: "1.5s"},
		{name: "preserves nanoseconds", input: time.Second + time.Nanosecond, want: "1.000000001s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatProtoDuration(tt.input); got != tt.want {
				t.Fatalf("formatProtoDuration(%s) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestXRayTraceIDFromW3CRequiresExactly128Bits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		traceID string
		want    string
	}{
		{name: "31 hex characters", traceID: strings.Repeat("a", 31)},
		{
			name:    "32 hex characters",
			traceID: "4efaaf4d1e8720b39541901950019ee5",
			want:    "1-4efaaf4d-1e8720b39541901950019ee5",
		},
		{name: "33 hex characters", traceID: strings.Repeat("a", 33)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := xrayTraceIDFromW3C(tt.traceID); got != tt.want {
				t.Fatalf("xrayTraceIDFromW3C(%q) = %q, want %q", tt.traceID, got, tt.want)
			}
		})
	}
}

func TestRequestPathRejectsUnavailableAndNonOriginForms(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.URL.Path = ""
	req.URL.Opaque = "/opaque"
	if got := requestPath(req); got != "" {
		t.Fatalf("opaque request path = %q, want omission", got)
	}
	req.URL.Opaque = ""
	if got := requestPath(req); got != "" {
		t.Fatalf("empty request path = %q, want omission", got)
	}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/widgets/a%2Fb?secret=true", nil)
	if got := requestPath(req); got != "/widgets/a%2Fb" {
		t.Fatalf("escaped request path = %q, want /widgets/a%%2Fb", got)
	}
	req.URL.Path = "/widgets/a/b"
	req.URL.RawPath = "/widgets/a%2G"
	if got := requestPath(req); got != "" {
		t.Fatalf("requestPath() repaired malformed raw path as %q", got)
	}
}

func TestDirectPeerIPNormalizesTransportAddressOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"empty", "", ""},
		{"IPv4 with port", "203.0.113.9:4321", "203.0.113.9"},
		{"IPv6 with port", "[2001:db8::1]:443", "2001:db8::1"},
		{"bare IPv6", "2001:db8::1", "2001:db8::1"},
		{"bracketed IPv6", "[2001:db8::1]", "2001:db8::1"},
		{"expanded IPv6", "2001:0db8:0:0:0:0:0:1", "2001:db8::1"},
		{"hostname with port", "example.com:443", ""},
		{"bare hostname", "peer.internal", ""},
		{"zoned IPv6", "[fe80::1%eth0]:443", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := directPeerIP(tt.addr); got != tt.want {
				t.Fatalf("directPeerIP(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestAccessLoggerResolvesAndValidatesGCPProfileVersionAtConstruction(t *testing.T) {
	t.Parallel()
	latest := normalizeAccessLoggerConfig(AccessLoggerConfig{Preset: PresetGCP})
	pinned := normalizeAccessLoggerConfig(AccessLoggerConfig{
		Preset:            PresetGCP,
		GCPProfileVersion: GCPProfileVersionV0_1_0,
	})
	if latest.GCPProfileVersion != GCPProfileVersionV0_1_0 ||
		pinned.GCPProfileVersion != GCPProfileVersionV0_1_0 {
		t.Fatalf(
			"resolved versions = (%q, %q), want %q",
			latest.GCPProfileVersion,
			pinned.GCPProfileVersion,
			GCPProfileVersionV0_1_0,
		)
	}

	tests := []struct {
		name   string
		config AccessLoggerConfig
		want   string
	}{
		{
			name:   "unsupported version",
			config: AccessLoggerConfig{Preset: PresetGCP, GCPProfileVersion: "0.2.0"},
			want:   `observability: unsupported GCP profile version "0.2.0"`,
		},
		{
			name:   "cross-preset version",
			config: AccessLoggerConfig{Preset: PresetAWS, GCPProfileVersion: GCPProfileVersionV0_1_0},
			want:   "observability: GCP profile version requires GCP preset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				recovered := recover()
				err, ok := recovered.(error)
				if !ok || err.Error() != tt.want {
					t.Fatalf("AccessLogger panic = %#v, want %q", recovered, tt.want)
				}
			}()
			AccessLogger(tt.config)
		})
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
