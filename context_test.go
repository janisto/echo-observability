package obs

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

func TestDefaultValidateRequestID(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"abc-XYZ_123.~":          true,
		"":                       false,
		strings.Repeat("a", 129): false,
		"abc def":                false,
		"abc/def":                false,
		"å":                      false,
	}
	for value, want := range tests {
		if got := DefaultValidateRequestID(value); got != want {
			t.Errorf("DefaultValidateRequestID(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestRequestContextRequestIDLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		incoming    string
		generated   string
		want        string
		disableResp bool
	}{
		{"valid incoming", "client-123", "generated-1", "client-123", false},
		{"missing", "", "generated-2", "generated-2", false},
		{"unsafe", "bad value", "generated-3", "generated-3", false},
		{"disabled response header", "client-4", "generated-4", "client-4", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{
				DisableResponseHeader: tt.disableResp,
				NewRequestID:          func() string { return tt.generated },
			}))
			var requestID, correlationID string
			e.GET("/test", func(c *echo.Context) error {
				requestID = RequestID(c.Request().Context())
				correlationID = CorrelationID(c.Request().Context())
				return c.NoContent(http.StatusNoContent)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
			if tt.incoming != "" {
				req.Header.Set(defaultRequestIDHeader, tt.incoming)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if requestID != tt.want || correlationID != tt.want {
				t.Fatalf("IDs = %q/%q, want %q", requestID, correlationID, tt.want)
			}
			wantHeader := tt.want
			if tt.disableResp {
				wantHeader = ""
			}
			if got := rec.Header().Get(defaultRequestIDHeader); got != wantHeader {
				t.Fatalf("response header = %q, want %q", got, wantHeader)
			}
		})
	}
}

func TestRequestContextTraceAndCustomHeaders(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{
		RequestIDHeader:   "X-Correlation-Request",
		TraceparentHeader: "X-Traceparent",
		TracestateHeader:  "X-Tracestate",
		ResponseHeader:    "X-Correlation-Response",
	}))
	var got TraceContext
	var correlation string
	e.GET("/test", func(c *echo.Context) error {
		got = Trace(c.Request().Context())
		correlation = CorrelationID(c.Request().Context())
		return nil
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	req.Header.Set("X-Correlation-Request", "req-1")
	req.Header.Set("X-Traceparent", traceparent)
	req.Header.Add("X-Tracestate", "vendor=value")
	req.Header.Add("X-Tracestate", "other=value")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if !got.Valid || !got.Sampled || got.Tracestate != "vendor=value,other=value" {
		t.Fatalf("unexpected trace: %#v", got)
	}
	if correlation != got.TraceID {
		t.Fatalf("correlation = %q, trace = %q", correlation, got.TraceID)
	}
	if rec.Header().Get("X-Correlation-Response") != "req-1" {
		t.Fatalf("response headers = %#v", rec.Header())
	}
}

func TestRequestContextTracestateBoundary(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	for _, size := range []int{maxTracestateLen, maxTracestateLen + 1} {
		e := echo.New()
		e.Use(RequestContext(RequestContextConfig{}))
		var got string
		e.GET("/", func(c *echo.Context) error {
			got = Trace(c.Request().Context()).Tracestate
			return nil
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set("Traceparent", traceparent)
		req.Header.Set("Tracestate", strings.Repeat("a", size))
		e.ServeHTTP(httptest.NewRecorder(), req)
		want := size
		if size > maxTracestateLen {
			want = 0
		}
		if len(got) != want {
			t.Fatalf("size %d produced tracestate length %d, want %d", size, len(got), want)
		}
	}
}

func TestRequestContextInstallsZapLogger(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("handler")
		return nil
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(defaultRequestIDHeader, "req-handler")
	e.ServeHTTP(httptest.NewRecorder(), req)
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["request_id"] != "req-handler" || entry["correlation_id"] != "req-handler" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}

func TestRequestContextPreservesExistingMetadataLogger(t *testing.T) {
	t.Parallel()
	var existingBuffer, configuredBuffer bytes.Buffer
	existing := newTestLogger(t, LoggerConfig{Writer: &existingBuffer})
	configured := newTestLogger(t, LoggerConfig{Writer: &configuredBuffer})
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing", Logger: existing}
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{Logger: configured}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("preserved")
		return nil
	})
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if !strings.Contains(existingBuffer.String(), "preserved") || configuredBuffer.Len() != 0 {
		t.Fatalf("existing=%q configured=%q", existingBuffer.String(), configuredBuffer.String())
	}
}

func TestNewValidRequestIDFallbacks(t *testing.T) {
	t.Parallel()
	calls := 0
	id := newValidRequestID(func() string {
		calls++
		return "invalid value"
	}, DefaultValidateRequestID)
	if calls != 2 || !DefaultValidateRequestID(id) || len(id) != 32 {
		t.Fatalf("calls=%d id=%q", calls, id)
	}

	constant := newValidRequestID(func() string { return "still invalid" }, func(string) bool { return false })
	if constant != "00000000000000000000000000000000" {
		t.Fatalf("constant fallback = %q", constant)
	}
}

func TestContextAccessorsWithoutMetadata(t *testing.T) {
	t.Parallel()
	//lint:ignore SA1012 Nil is part of the accessor contract under test.
	if RequestID(nil) != "" { //nolint:staticcheck // verifies nil-safety contract
		t.Fatal("nil context returned a request ID")
	}
	//lint:ignore SA1012 Nil is part of the accessor contract under test.
	if CorrelationID(nil) != "" { //nolint:staticcheck // verifies nil-safety contract
		t.Fatal("nil context returned a correlation ID")
	}
	//lint:ignore SA1012 Nil is part of the accessor contract under test.
	if Trace(nil).Valid { //nolint:staticcheck // verifies nil-safety contract
		t.Fatal("nil context returned a valid trace")
	}
}

func TestHTTPRequestContextLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		incoming   string
		generated  string
		want       string
		disable    bool
		customName string
	}{
		{name: "valid incoming", incoming: "client-123", generated: "generated-1", want: "client-123"},
		{name: "missing", generated: "generated-2", want: "generated-2"},
		{name: "invalid", incoming: "bad value", generated: "generated-3", want: "generated-3"},
		{name: "disabled response", incoming: "client-4", want: "client-4", disable: true},
		{name: "custom header", incoming: "custom-5", want: "custom-5", customName: "X-Correlation-Request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			header := tt.customName
			if header == "" {
				header = defaultRequestIDHeader
			}
			var gotRequestID, gotCorrelationID string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRequestID = RequestID(r.Context())
				gotCorrelationID = CorrelationID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			handler := HTTPRequestContext(HTTPRequestContextConfig{
				RequestIDHeader:       header,
				ResponseHeader:        header,
				DisableResponseHeader: tt.disable,
				NewRequestID:          func() string { return tt.generated },
			})(next)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
			if tt.incoming != "" {
				req.Header.Set(header, tt.incoming)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if gotRequestID != tt.want || gotCorrelationID != tt.want {
				t.Fatalf("IDs = %q/%q, want %q", gotRequestID, gotCorrelationID, tt.want)
			}
			wantHeader := tt.want
			if tt.disable {
				wantHeader = ""
			}
			if got := rec.Header().Get(header); got != wantHeader {
				t.Fatalf("response header = %q, want %q", got, wantHeader)
			}
		})
	}
}

func TestHTTPRequestContextTraceAndTracestate(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name          string
		traceparent   string
		tracestate    string
		wantValid     bool
		wantStateSize int
	}{
		{
			name:          "valid",
			traceparent:   traceparent,
			tracestate:    "vendor=value",
			wantValid:     true,
			wantStateSize: len("vendor=value"),
		},
		{
			name:        "overlong state",
			traceparent: traceparent,
			tracestate:  strings.Repeat("a", maxTracestateLen+1),
			wantValid:   true,
		},
		{name: "invalid trace", traceparent: "invalid", tracestate: "vendor=value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got TraceContext
			handler := HTTPRequestContext(HTTPRequestContextConfig{
				TraceparentHeader: "X-Traceparent",
				TracestateHeader:  "X-Tracestate",
			})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = Trace(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
			req.Header.Set("X-Traceparent", tt.traceparent)
			req.Header.Set("X-Tracestate", tt.tracestate)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if got.Valid != tt.wantValid || len(got.Tracestate) != tt.wantStateSize {
				t.Fatalf("trace = %#v, want valid=%v state size=%d", got, tt.wantValid, tt.wantStateSize)
			}
		})
	}
}

func TestHTTPRequestContextCombinesTracestateHeaders(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	var got TraceContext
	handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			got = Trace(r.Context())
		},
	))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	req.Header.Set("Traceparent", traceparent)
	req.Header.Add("Tracestate", "vendor=value")
	req.Header.Add("Tracestate", "other=value")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if got.Tracestate != "vendor=value,other=value" {
		t.Fatalf("tracestate = %q", got.Tracestate)
	}
}

func TestHTTPRequestContextInstallsProviderLogger(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		Logger: logger,
		Preset: PresetGCP,
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		Logger(r.Context()).Info("http handler")
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/test", nil)
	req.Header.Set(defaultRequestIDHeader, "req-http")
	req.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"severity": "INFO", "request_id": "req-http", "correlation_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"logging.googleapis.com/trace": "4bf92f3577b34da6a3ce929d0e0e4736",
	})
}

func TestHTTPRequestContextMetadataReuseAndImmutability(t *testing.T) {
	t.Parallel()
	var existingBuffer, configuredBuffer bytes.Buffer
	existing := newTestLogger(t, LoggerConfig{Writer: &existingBuffer})
	configured := newTestLogger(t, LoggerConfig{Writer: &configuredBuffer})
	metadata := &requestMetadata{
		RequestID: "existing-req", CorrelationID: "existing-corr",
		Logger: existing.With(zap.String("source", "existing")),
	}
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	handler := HTTPRequestContext(
		HTTPRequestContextConfig{Logger: configured},
	)(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if RequestID(r.Context()) != "existing-req" || CorrelationID(r.Context()) != "existing-corr" {
				t.Fatalf("metadata was replaced: %q/%q", RequestID(r.Context()), CorrelationID(r.Context()))
			}
			Logger(r.Context()).Info("reused")
		}),
	)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if metadata.Logger == nil || !strings.Contains(existingBuffer.String(), "reused") || configuredBuffer.Len() != 0 {
		t.Fatalf("existing=%q configured=%q metadata=%#v", existingBuffer.String(), configuredBuffer.String(), metadata)
	}
}

func TestHTTPRequestContextCompletesLoggerOnlyMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	existing := newTestLogger(t, LoggerConfig{Writer: &buffer})
	original := &requestMetadata{Logger: existing.With(zap.String("source", "outer"))}
	ctx := contextWithRequestMetadata(context.Background(), original)
	handler := HTTPRequestContext(HTTPRequestContextConfig{
		NewRequestID: func() string { return "generated-http" },
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) != "generated-http" {
			t.Fatalf("request ID = %q", RequestID(r.Context()))
		}
		Logger(r.Context()).Info("completed")
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if original.RequestID != "" || original.CorrelationID != "" || !strings.Contains(buffer.String(), "completed") {
		t.Fatalf("input mutated or logger missing: original=%#v logs=%q", original, buffer.String())
	}
}

func TestRequestContextReusesHTTPMetadata(t *testing.T) {
	t.Parallel()
	var generated int
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{
		NewRequestID: func() string {
			generated++
			return "echo-generated"
		},
	}))
	e.GET("/", func(c *echo.Context) error {
		if RequestID(c.Request().Context()) != "outer-http" {
			t.Fatalf("request ID = %q", RequestID(c.Request().Context()))
		}
		return c.NoContent(http.StatusNoContent)
	})
	handler := HTTPRequestContext(HTTPRequestContextConfig{})(e)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(defaultRequestIDHeader, "outer-http")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if generated != 0 {
		t.Fatalf("Echo middleware generated %d replacement IDs", generated)
	}
}

func TestRequestContextLoggerInstallDoesNotMutateMetadata(t *testing.T) {
	t.Parallel()
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing"}
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{Logger: zap.NewNop()}))
	e.GET("/", func(c *echo.Context) error {
		if Logger(c.Request().Context()) == nil {
			t.Fatal("logger is nil")
		}
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	if metadata.Logger != nil {
		t.Fatal("input metadata was mutated")
	}
}
