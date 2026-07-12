package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func TestNewLoggerPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		preset Preset
		key    string
		value  string
	}{
		{PresetDefault, "level", "INFO"},
		{PresetGCP, "severity", "INFO"},
		{PresetAWS, "level", "INFO"},
		{PresetAzure, "level", "INFO"},
	}
	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			t.Parallel()
			var buffer bytes.Buffer
			logger, err := NewLogger(LoggerConfig{Preset: tt.preset, Writer: &buffer})
			if err != nil {
				t.Fatal(err)
			}
			logger.Info("hello", zap.String("component", "test"))
			entry := decodeSingleLogLine(t, buffer.String())
			if entry[tt.key] != tt.value || entry["message"] != "hello" || entry["component"] != "test" {
				t.Fatalf("unexpected entry: %#v", entry)
			}
			if _, ok := entry["timestamp"].(string); !ok {
				t.Fatalf("timestamp missing: %#v", entry)
			}
		})
	}
}

func TestNewLoggerRejectsUnknownPreset(t *testing.T) {
	t.Parallel()
	if _, err := NewLogger(LoggerConfig{Preset: "bogus"}); err == nil {
		t.Fatal("NewLogger accepted an unknown preset")
	}
}

func TestNewLoggerWritesNamedLoggerField(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	logger.Named("worker").Info("named log")
	if got := decodeSingleLogLine(t, buffer.String())["logger"]; got != "worker" {
		t.Fatalf("logger = %v, want worker", got)
	}
}

func TestRequestLoggerFieldsIncludeTraceOnlyWhenValid(t *testing.T) {
	t.Parallel()
	for _, trace := range []TraceContext{{}, {
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ParentID: "00f067aa0ba902b7",
		Flags: "01", Sampled: true, Valid: true,
	}} {
		var buffer bytes.Buffer
		base, err := NewLogger(LoggerConfig{Writer: &buffer})
		if err != nil {
			t.Fatal(err)
		}
		base.With(requestMetadataFields(&requestMetadata{
			RequestID: "req-1", CorrelationID: "corr-1", Trace: trace,
		})...).Info("handler")
		entry := decodeSingleLogLine(t, buffer.String())
		_, hasTraceID := entry["trace_id"]
		if hasTraceID != trace.Valid {
			t.Fatalf("trace_id present=%v valid=%v entry=%#v", hasTraceID, trace.Valid, entry)
		}
		if trace.Valid && entry["trace_sampled"] != true {
			t.Fatalf("trace fields = %#v", entry)
		}
	}
}

func TestGCPLevelMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		log  func(*zap.Logger)
		want string
	}{
		{func(l *zap.Logger) { l.Debug("level") }, "DEBUG"},
		{func(l *zap.Logger) { l.Info("level") }, "INFO"},
		{func(l *zap.Logger) { l.Warn("level") }, "WARNING"},
		{func(l *zap.Logger) { l.Error("level") }, "ERROR"},
	}
	for _, tt := range tests {
		var buffer bytes.Buffer
		logger, err := NewLogger(LoggerConfig{Preset: PresetGCP, Writer: &buffer, Level: zap.DebugLevel})
		if err != nil {
			t.Fatal(err)
		}
		tt.log(logger)
		if got := decodeSingleLogLine(t, buffer.String())["severity"]; got != tt.want {
			t.Fatalf("severity = %v, want %s", got, tt.want)
		}
	}
}

func TestNewLoggerConcurrentWrites(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	const goroutines, writes = 8, 25
	var wg sync.WaitGroup
	for worker := range goroutines {
		wg.Go(func() {
			for write := range writes {
				logger.Info("concurrent", zap.Int("worker", worker), zap.Int("write", write))
			}
		})
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != goroutines*writes {
		t.Fatalf("line count = %d", len(lines))
	}
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	}
}

func TestLoggerAccessor(t *testing.T) {
	t.Parallel()
	//lint:ignore SA1012 Nil is part of the accessor contract under test.
	if Logger(nil) == nil || Logger(context.Background()) == nil { //nolint:staticcheck // verifies nil-safety contract
		t.Fatal("Logger returned nil")
	}
	var buffer bytes.Buffer
	base, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	ctx := contextWithRequestMetadata(context.Background(), &requestMetadata{
		RequestID: "req-1",
		Logger:    base.With(zap.String("request_id", "req-1")),
	})
	Logger(ctx).Info("handler")
	if entry := decodeSingleLogLine(t, buffer.String()); entry["request_id"] != "req-1" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
}

func decodeSingleLogLine(t *testing.T, line string) map[string]any {
	t.Helper()
	line = strings.TrimSpace(line)
	if line == "" {
		t.Fatal("expected log line")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("invalid log JSON: %v\n%s", err, line)
	}
	return entry
}
