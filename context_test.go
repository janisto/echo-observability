package obs

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
)

func TestDefaultValidateRequestIDEnforcesASCIIAndLengthBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "allowed character boundaries", value: "azAZ09-._~", want: true},
		{name: "maximum length", value: strings.Repeat("a", 128), want: true},
		{name: "empty", value: ""},
		{name: "over maximum length", value: strings.Repeat("a", 129)},
		{name: "before lowercase", value: "`"},
		{name: "after lowercase", value: "{"},
		{name: "before uppercase", value: "@"},
		{name: "after uppercase", value: "["},
		{name: "before digit", value: "/"},
		{name: "after digit", value: ":"},
		{name: "embedded space", value: "abc def"},
		{name: "embedded slash", value: "abc/def"},
		{name: "non ASCII", value: "å"},
	}
	for _, tt := range tests {
		if got := DefaultValidateRequestID(tt.value); got != tt.want {
			t.Errorf("%s: DefaultValidateRequestID(%q) = %v, want %v", tt.name, tt.value, got, tt.want)
		}
	}
}

func TestRequestContextUsesCustomValidatorAndDefaultsResponseHeader(t *testing.T) {
	t.Parallel()
	const header = "X-Custom-Request-Id"
	const generated = "generated"

	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{
		RequestIDHeader: header,
		NewRequestID:    func() string { return generated },
		ValidateRequestID: func(value string) bool {
			return strings.HasPrefix(value, "custom-")
		},
	}))
	var got string
	e.GET("/", func(c *echo.Context) error {
		got = RequestID(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(header, "client-123")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if got != generated || rec.Header().Get(header) != generated {
		t.Fatalf("request ID/header = %q/%q, want %q", got, rec.Header().Get(header), generated)
	}
	if value := rec.Header().Get(defaultRequestIDHeader); value != "" {
		t.Fatalf("unexpected default response header = %q", value)
	}
}

func TestRequestContextCustomValidatorMayBroadenNativeSafeRequestIDs(t *testing.T) {
	t.Parallel()
	for _, requestID := range []string{"id:42", strings.Repeat("a", 129), "tenant request", "tenant\trequest", "native-\u0081"} {
		e := echo.New()
		e.Use(RequestContext(RequestContextConfig{
			ValidateRequestID: func(string) bool { return true },
		}))
		var got string
		e.GET("/", func(c *echo.Context) error {
			got = RequestID(c.Request().Context())
			return c.NoContent(http.StatusNoContent)
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set(defaultRequestIDHeader, requestID)
		e.ServeHTTP(httptest.NewRecorder(), req)
		if got != requestID {
			t.Fatalf("request ID = %q, want custom-admitted %q", got, requestID)
		}
	}
}

func TestRequestContextRequestIDLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		incoming      string
		generated     string
		want          string
		disableResp   bool
		wantGenerated int
	}{
		{name: "valid incoming", incoming: "client-123", generated: "generated-1", want: "client-123"},
		{name: "missing", generated: "generated-2", want: "generated-2", wantGenerated: 1},
		{name: "unsafe", incoming: "bad value", generated: "generated-3", want: "generated-3", wantGenerated: 1},
		{
			name: "disabled response header", incoming: "client-4", generated: "generated-4",
			want: "client-4", disableResp: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			generatedCalls := 0
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{
				DisableResponseHeader: tt.disableResp,
				NewRequestID: func() string {
					generatedCalls++
					return tt.generated
				},
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
			if generatedCalls != tt.wantGenerated {
				t.Fatalf("NewRequestID calls = %d, want %d", generatedCalls, tt.wantGenerated)
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
	valid512 := "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 251)
	for _, tracestate := range []string{valid512, valid512 + "w"} {
		e := echo.New()
		e.Use(RequestContext(RequestContextConfig{}))
		var got string
		e.GET("/", func(c *echo.Context) error {
			got = Trace(c.Request().Context()).Tracestate
			return nil
		})
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set("Traceparent", traceparent)
		req.Header.Set("Tracestate", tracestate)
		e.ServeHTTP(httptest.NewRecorder(), req)
		if got != tracestate {
			t.Fatalf(
				"size %d: preserved input=%v, got length=%d, want length=%d",
				len(tracestate),
				got == tracestate,
				len(got),
				len(tracestate),
			)
		}
	}
}

func TestRequestContextRejectsDuplicateRequestIDAndTraceparentFieldLines(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name   string
		header string
		values []string
	}{
		{name: "identical request IDs", header: defaultRequestIDHeader, values: []string{"caller", "caller"}},
		{name: "different request IDs", header: defaultRequestIDHeader, values: []string{"caller", "other"}},
		{name: "identical traceparents", header: defaultTraceparentHeader, values: []string{traceparent, traceparent}},
		{
			name:   "different traceparents",
			header: defaultTraceparentHeader,
			values: []string{traceparent, strings.Replace(traceparent, "-01", "-00", 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := echo.New()
			e.Use(RequestContext(RequestContextConfig{NewRequestID: func() string { return "generated" }}))
			var requestID string
			var trace TraceContext
			e.GET("/", func(c *echo.Context) error {
				requestID = RequestID(c.Request().Context())
				trace = Trace(c.Request().Context())
				return c.NoContent(http.StatusNoContent)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header.Set(defaultRequestIDHeader, "caller")
			if tt.header == defaultTraceparentHeader {
				req.Header.Set(defaultRequestIDHeader, "request")
			}
			req.Header[http.CanonicalHeaderKey(tt.header)] = append([]string(nil), tt.values...)
			e.ServeHTTP(httptest.NewRecorder(), req)
			if tt.header == defaultRequestIDHeader {
				if requestID != "generated" || trace.Valid {
					t.Fatalf("duplicate request ID selected: request_id=%q trace=%#v", requestID, trace)
				}
				return
			}
			if requestID != "request" || trace.Valid {
				t.Fatalf("duplicate traceparent selected: request_id=%q trace=%#v", requestID, trace)
			}
		})
	}
}

func TestRequestContextLevel2AndConstructionValidation(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-03"
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{TraceContextLevel: TraceContextLevel2}))
	var got TraceContext
	e.GET("/", func(c *echo.Context) error {
		got = Trace(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("Traceparent", traceparent)
	e.ServeHTTP(httptest.NewRecorder(), req)
	if !got.Valid || got.Level != TraceContextLevel2 || !got.Sampled || !got.Random {
		t.Fatalf("Level 2 request trace = %#v", got)
	}

	defer func() {
		value := recover()
		if fmt.Sprint(value) != "unsupported trace context level 3: supported levels are 1 and 2" {
			t.Fatalf("invalid-level panic = %v", value)
		}
	}()
	_ = RequestContext(RequestContextConfig{TraceContextLevel: 3})
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
	existingLogger := existing.With(zap.String("source", "existing"))
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing", Logger: existingLogger}
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{Logger: configured}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("preserved")
		return nil
	})
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, existingBuffer.String())
	if entry["message"] != "preserved" || entry["source"] != "existing" || configuredBuffer.Len() != 0 {
		t.Fatalf("existing=%#v configured=%q", entry, configuredBuffer.String())
	}
	if metadata.RequestID != "existing" || metadata.CorrelationID != "existing" || metadata.Logger != existingLogger {
		t.Fatalf("incoming metadata was mutated: %#v", metadata)
	}
}

func TestNewValidRequestIDUsesSafeDefaultFormatWhenCustomGenerationFails(t *testing.T) {
	t.Parallel()
	calls := 0
	id := newValidRequestID(func() string {
		calls++
		return "invalid value"
	})
	decoded, err := hex.DecodeString(id)
	if calls != 2 || err != nil || len(decoded) != 16 || id != strings.ToLower(id) {
		t.Errorf("calls=%d id=%q decode_error=%v", calls, id, err)
	}
}

func TestNativeRequestIDBoundaryAllowsOnlyHTTPFieldText(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"~", "tenant request", "tenant\trequest", "\u0080", "\u00ff"} {
		if !nativeSafeRequestID(value) {
			t.Fatalf("native-safe request ID boundary %q was rejected", value)
		}
	}
	for _, value := range []string{"", " ", "\t", " tenant", "tenant ", "\ttenant", "tenant\t", "\x00", "\x1f", "\x7f", "\x80", "\xff"} {
		if nativeSafeRequestID(value) {
			t.Fatalf("unsafe request ID boundary %q was accepted", value)
		}
	}
}

func TestCustomRequestIDValidatorRunsOnlyForRFCFieldContent(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"tenant request", "tenant\trequest", "tenant,request"} {
		calls := 0
		if !validIncomingRequestID(value, func(string) bool { calls++; return true }) || calls != 1 {
			t.Fatalf("valid field content %q: accepted=false or calls=%d", value, calls)
		}
	}
	for _, value := range []string{" tenant", "tenant ", "\x80"} {
		calls := 0
		if validIncomingRequestID(value, func(string) bool { calls++; return true }) || calls != 0 {
			t.Fatalf("unsafe field content %q reached validator %d times", value, calls)
		}
	}
}

func TestRequestIDHookPanicsAreContainedAndPreserveHandler(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		incoming string
		config   RequestContextConfig
	}{
		{
			name:     "validator",
			incoming: "caller",
			config: RequestContextConfig{
				NewRequestID: func() string { return "generated" },
				ValidateRequestID: func(string) bool {
					panic("validator secret")
				},
			},
		},
		{
			name: "generator",
			config: RequestContextConfig{NewRequestID: func() string {
				panic("generator secret")
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := echo.New()
			e.Use(RequestContext(tt.config))
			handlerCalled := false
			var got string
			e.GET("/", func(c *echo.Context) error {
				handlerCalled = true
				got = RequestID(c.Request().Context())
				return c.NoContent(http.StatusNoContent)
			})
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.incoming != "" {
				req.Header.Set(defaultRequestIDHeader, tt.incoming)
			}
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, req)
			if !handlerCalled || !DefaultValidateRequestID(got) {
				t.Fatalf("handler_called=%v request_id=%q", handlerCalled, got)
			}
			if recorder.Header().Get(defaultRequestIDHeader) != got {
				t.Fatalf("response request ID does not match handler ID: %#v", recorder.Header())
			}
		})
	}
}

func TestDefaultNewRequestIDProducesUniqueLowercase128BitValues(t *testing.T) {
	t.Parallel()
	const count = 64
	seen := make(map[string]struct{}, count)
	for range count {
		id := defaultNewRequestID()
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 16 || id != strings.ToLower(id) {
			t.Fatalf("generated request ID %q is not 32 lowercase hexadecimal characters", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("generated duplicate request ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestContextAccessorsWithoutMetadata(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "nil"},
		{name: "background", ctx: context.Background()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requestID := RequestID(tt.ctx)
			correlationID := CorrelationID(tt.ctx)
			if requestID != "" || correlationID != "" {
				t.Fatalf("request/correlation IDs = %q/%q, want empty", requestID, correlationID)
			}
			if trace := Trace(tt.ctx); trace != (TraceContext{}) {
				t.Fatalf("trace = %#v, want zero value", trace)
			}
		})
	}
}

func TestHTTPRequestContextLifecycle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		incoming      string
		generated     string
		want          string
		disable       bool
		customName    string
		responseName  string
		validate      func(string) bool
		wantGenerated int
	}{
		{name: "valid incoming", incoming: "client-123", generated: "generated-1", want: "client-123"},
		{name: "missing", generated: "generated-2", want: "generated-2", wantGenerated: 1},
		{name: "invalid", incoming: "bad value", generated: "generated-3", want: "generated-3", wantGenerated: 1},
		{name: "disabled response", incoming: "client-4", want: "client-4", disable: true},
		{name: "custom header", incoming: "custom-5", want: "custom-5", customName: "X-Correlation-Request"},
		{
			name: "custom response header", incoming: "client-6", want: "client-6",
			customName: "X-Correlation-Request", responseName: "X-Correlation-Response",
		},
		{
			name: "custom validator", incoming: "client-123", generated: "custom-id", want: "custom-id",
			validate:      func(value string) bool { return strings.HasPrefix(value, "custom-") },
			wantGenerated: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			generatedCalls := 0
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
				ResponseHeader:        tt.responseName,
				DisableResponseHeader: tt.disable,
				NewRequestID: func() string {
					generatedCalls++
					return tt.generated
				},
				ValidateRequestID: tt.validate,
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
			responseHeader := tt.responseName
			if responseHeader == "" {
				responseHeader = header
			}
			wantHeader := tt.want
			if tt.disable {
				wantHeader = ""
			}
			if got := rec.Header().Get(responseHeader); got != wantHeader {
				t.Fatalf("response header = %q, want %q", got, wantHeader)
			}
			if generatedCalls != tt.wantGenerated {
				t.Fatalf("NewRequestID calls = %d, want %d", generatedCalls, tt.wantGenerated)
			}
		})
	}
}

type callerContextKey struct{}

func TestRequestContextMiddlewarePreservesCallerValueCancellationAndDeadline(t *testing.T) {
	t.Parallel()

	t.Run("Echo", func(t *testing.T) {
		t.Parallel()
		parent, deadline := canceledCallerContext(t)
		e := echo.New()
		e.Use(RequestContext(RequestContextConfig{}))
		e.GET("/", func(c *echo.Context) error {
			assertCallerContextPreserved(t, c.Request().Context(), deadline)
			return c.NoContent(http.StatusNoContent)
		})
		e.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequestWithContext(parent, http.MethodGet, "/", nil),
		)
	})

	t.Run("net/http", func(t *testing.T) {
		t.Parallel()
		parent, deadline := canceledCallerContext(t)
		handler := HTTPRequestContext(HTTPRequestContextConfig{})(http.HandlerFunc(
			func(_ http.ResponseWriter, request *http.Request) {
				assertCallerContextPreserved(t, request.Context(), deadline)
			},
		))
		handler.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequestWithContext(parent, http.MethodGet, "/http", nil),
		)
	})
}

func canceledCallerContext(t *testing.T) (context.Context, time.Time) {
	t.Helper()
	deadline := time.Now().Add(time.Hour)
	parent, cancel := context.WithDeadline(
		context.WithValue(context.Background(), callerContextKey{}, "sentinel"),
		deadline,
	)
	cancel()
	return parent, deadline
}

func assertCallerContextPreserved(t *testing.T, ctx context.Context, deadline time.Time) {
	t.Helper()
	if got := ctx.Value(callerContextKey{}); got != "sentinel" {
		t.Fatalf("caller context value = %#v, want sentinel", got)
	}
	gotDeadline, ok := ctx.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("caller deadline = (%v, %v), want %v", gotDeadline, ok, deadline)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("caller cancellation = %v, want context.Canceled", ctx.Err())
	}
}

func TestHTTPRequestContextTraceAndTracestate(t *testing.T) {
	t.Parallel()
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tests := []struct {
		name           string
		traceparent    string
		tracestate     string
		wantValid      bool
		wantTracestate string
	}{
		{
			name:           "valid",
			traceparent:    traceparent,
			tracestate:     "vendor=value",
			wantValid:      true,
			wantTracestate: "vendor=value",
		},
		{
			name:           "513 character state",
			traceparent:    traceparent,
			tracestate:     "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 252),
			wantValid:      true,
			wantTracestate: "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 252),
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
			if got.Valid != tt.wantValid || got.Tracestate != tt.wantTracestate {
				t.Fatalf(
					"trace = %#v, want valid=%v tracestate=%q",
					got,
					tt.wantValid,
					tt.wantTracestate,
				)
			}
			if tt.wantValid && got.Traceparent != traceparent {
				t.Fatalf("traceparent = %q, want %q", got.Traceparent, traceparent)
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
	existingLogger := existing.With(zap.String("source", "existing"))
	metadata := &requestMetadata{
		RequestID: "existing-req", CorrelationID: "existing-corr",
		Logger: existingLogger,
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
	entry := decodeSingleLogLine(t, existingBuffer.String())
	if entry["message"] != "reused" || entry["source"] != "existing" || configuredBuffer.Len() != 0 {
		t.Fatalf("existing=%#v configured=%q", entry, configuredBuffer.String())
	}
	if metadata.RequestID != "existing-req" ||
		metadata.CorrelationID != "existing-corr" ||
		metadata.Logger != existingLogger {
		t.Fatalf("incoming metadata was mutated: %#v", metadata)
	}
}

func TestHTTPRequestContextCompletesLoggerOnlyMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	existing := newTestLogger(t, LoggerConfig{Writer: &buffer})
	originalLogger := existing.With(zap.String("source", "outer"))
	original := &requestMetadata{Logger: originalLogger}
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
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"message": "completed", "source": "outer",
		"request_id": "generated-http", "correlation_id": "generated-http",
	})
	if original.RequestID != "" || original.CorrelationID != "" || original.Logger != originalLogger {
		t.Fatalf("incoming metadata was mutated: %#v", original)
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

func TestRequestContextInstallsLoggerWithoutMutatingExistingMetadata(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	metadata := &requestMetadata{RequestID: "existing", CorrelationID: "existing"}
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{Logger: logger}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("installed")
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"message": "installed", "request_id": "existing", "correlation_id": "existing",
	})
	if metadata.Logger != nil {
		t.Fatal("input metadata was mutated")
	}
}

func TestRequestContextCompletesEmptyMetadataWithConfiguredLogger(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := newTestLogger(t, LoggerConfig{Writer: &buffer})
	metadata := &requestMetadata{}
	ctx := contextWithRequestMetadata(context.Background(), metadata)
	e := echo.New()
	e.Use(RequestContext(RequestContextConfig{
		Logger:       logger,
		NewRequestID: func() string { return "generated-empty" },
	}))
	e.GET("/", func(c *echo.Context) error {
		Logger(c.Request().Context()).Info("completed empty metadata")
		return nil
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	entry := decodeSingleLogLine(t, buffer.String())
	assertFields(t, entry, map[string]any{
		"message":        "completed empty metadata",
		"request_id":     "generated-empty",
		"correlation_id": "generated-empty",
	})
	if metadata.RequestID != "" || metadata.CorrelationID != "" || metadata.Logger != nil {
		t.Fatalf("input metadata was mutated: %#v", metadata)
	}
}
