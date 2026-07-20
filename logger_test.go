package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestNewLoggerPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		preset Preset
		key    string
		value  string
	}{
		{"default", PresetDefault, "level", "INFO"},
		{"gcp", PresetGCP, "severity", "INFO"},
		{"aws", PresetAWS, "level", "INFO"},
		{"azure", PresetAzure, "level", "INFO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			otherLevelKey := "severity"
			if tt.key == "severity" {
				otherLevelKey = "level"
			}
			if _, ok := entry[otherLevelKey]; ok {
				t.Fatalf("unexpected %s in entry: %#v", otherLevelKey, entry)
			}
			if _, ok := entry["timestamp"].(string); !ok {
				t.Fatalf("timestamp missing: %#v", entry)
			}
		})
	}
}

func TestNewLoggerWritesLFTerminatedNDJSONRecords(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger, err := NewLogger(LoggerConfig{Writer: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("first\nlogical message")
	logger.Error("second message")

	output := buffer.String()
	if !strings.HasSuffix(output, "\n") || strings.Contains(output, "\r") {
		t.Fatalf("output is not LF-terminated NDJSON: %q", output)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("physical line count = %d, want 2; output=%q", len(lines), output)
	}
	wantMessages := []string{"first\nlogical message", "second message"}
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not one JSON object: %v; line=%q", index, err, line)
		}
		if got := record["message"]; got != wantMessages[index] {
			t.Fatalf("line %d message = %#v, want %q", index, got, wantMessages[index])
		}
	}
}

func TestNewLoggerRejectsUnknownPreset(t *testing.T) {
	t.Parallel()
	logger, err := NewLogger(LoggerConfig{Preset: "bogus"})
	if logger != nil || err == nil || err.Error() != "observability: unknown logger preset" {
		t.Fatalf("NewLogger(bogus) = (%#v, %v), want nil and exact unknown-preset error", logger, err)
	}
}

func TestGCPProfileVersionResolutionAndLoggerValidation(t *testing.T) {
	t.Parallel()
	if GCPProfileVersionV0_1_0 != GCPProfileVersion("0.1.0") {
		t.Fatalf("GCPProfileVersionV0_1_0 = %q, want literal 0.1.0", GCPProfileVersionV0_1_0)
	}
	latest, err := ResolveGCPProfileVersion(PresetGCP, "")
	if err != nil || latest != GCPProfileVersion("0.1.0") {
		t.Fatalf("latest GCP profile = (%q, %v), want literal 0.1.0", latest, err)
	}
	pinned, err := ResolveGCPProfileVersion(PresetGCP, GCPProfileVersionV0_1_0)
	if err != nil || pinned != GCPProfileVersionV0_1_0 {
		t.Fatalf("pinned GCP profile = (%q, %v), want %q", pinned, err, GCPProfileVersionV0_1_0)
	}

	tests := []struct {
		name   string
		config LoggerConfig
		want   string
	}{
		{
			name:   "unsupported version",
			config: LoggerConfig{Preset: PresetGCP, GCPProfileVersion: "0.2.0"},
			want:   `observability: unsupported GCP profile version "0.2.0"`,
		},
		{
			name:   "cross-preset version",
			config: LoggerConfig{Preset: PresetAWS, GCPProfileVersion: GCPProfileVersionV0_1_0},
			want:   "observability: GCP profile version requires GCP preset",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger, err := NewLogger(tt.config)
			if logger != nil || err == nil || err.Error() != tt.want {
				t.Fatalf("NewLogger(invalid) = (%#v, %v), want nil and %q", logger, err, tt.want)
			}
		})
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
	for _, trace := range []TraceContext{
		{},
		{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ParentID: "00f067aa0ba902b7",
			Version: "00", Flags: "01", Sampled: true, Level: TraceContextLevel1, Valid: true,
		},
		{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ParentID: "00f067aa0ba902b7",
			Version: "00", Flags: "03", Sampled: true, Random: true, Level: TraceContextLevel2, Valid: true,
		},
		{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", ParentID: "00f067aa0ba902b7",
			Version: "01", Flags: "03", Sampled: true, Level: TraceContextLevel2, Valid: true,
		},
	} {
		var buffer bytes.Buffer
		base, err := NewLogger(LoggerConfig{Writer: &buffer})
		if err != nil {
			t.Fatal(err)
		}
		base.With(requestMetadataFields(&requestMetadata{
			RequestID: "req-1", CorrelationID: "corr-1", Trace: trace,
		})...).Info("handler")
		entry := decodeSingleLogLine(t, buffer.String())
		if entry["request_id"] != "req-1" || entry["correlation_id"] != "corr-1" {
			t.Fatalf("request metadata fields = %#v", entry)
		}
		traceFields := []string{"trace_id", "parent_id", "trace_flags", "trace_sampled", "trace_id_random"}
		if !trace.Valid {
			for _, key := range traceFields {
				if _, ok := entry[key]; ok {
					t.Fatalf("invalid trace emitted %s: %#v", key, entry)
				}
			}
			continue
		}
		want := map[string]any{
			"trace_id": trace.TraceID, "parent_id": trace.ParentID,
			"trace_flags": trace.Flags, "trace_sampled": trace.Sampled,
		}
		for key, value := range want {
			if entry[key] != value {
				t.Fatalf("%s = %#v, want %#v; entry=%#v", key, entry[key], value, entry)
			}
		}
		if trace.Level == TraceContextLevel2 && trace.Version == "00" {
			if entry["trace_id_random"] != trace.Random {
				t.Fatalf("trace_id_random = %#v, want %#v; entry=%#v", entry["trace_id_random"], trace.Random, entry)
			}
		} else if _, ok := entry["trace_id_random"]; ok {
			t.Fatalf("trace without version 00 Level 2 semantics emitted trace_id_random: %#v", entry)
		}
	}
}

func TestGCPLevelMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level zapcore.Level
		want  string
	}{
		{zapcore.DebugLevel, "DEBUG"},
		{zapcore.InfoLevel, "INFO"},
		{zapcore.WarnLevel, "WARNING"},
		{zapcore.ErrorLevel, "ERROR"},
		{zapcore.DPanicLevel, "CRITICAL"},
		{zapcore.PanicLevel, "CRITICAL"},
		{zapcore.FatalLevel, "CRITICAL"},
		{zapcore.Level(-99), "DEBUG"},
		{zapcore.Level(99), "CRITICAL"},
	}
	for _, tt := range tests {
		encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			LevelKey:    "severity",
			MessageKey:  "message",
			LineEnding:  zapcore.DefaultLineEnding,
			EncodeLevel: gcpLevelEncoder,
		})
		buffer, err := encoder.EncodeEntry(zapcore.Entry{Level: tt.level, Message: "level"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		entry := decodeSingleLogLine(t, buffer.String())
		buffer.Free()
		if got := entry["severity"]; got != tt.want {
			t.Fatalf("severity = %v, want %s", got, tt.want)
		}
	}
}

func TestLoggerTimestampIsUTC(t *testing.T) {
	t.Parallel()
	encoder := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		TimeKey:    "timestamp",
		LineEnding: zapcore.DefaultLineEnding,
		EncodeTime: utcRFC3339NanoTimeEncoder,
	})
	entryTime := time.Date(2026, 7, 15, 12, 34, 56, 123456789, time.FixedZone("UTC+3", 3*60*60))
	buffer, err := encoder.EncodeEntry(zapcore.Entry{Time: entryTime}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := decodeSingleLogLine(t, buffer.String())
	buffer.Free()
	if got := entry["timestamp"]; got != "2026-07-15T09:34:56.123456789Z" {
		t.Fatalf("timestamp = %v", got)
	}
}

func TestNewLoggerReportsSinkFailuresToErrorWriter(t *testing.T) {
	t.Parallel()
	var errorOutput bytes.Buffer
	logger, err := NewLogger(LoggerConfig{
		Writer:      failingWriter{err: errors.New("sink unavailable")},
		ErrorWriter: &errorOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("lost log")
	if output := errorOutput.String(); !strings.Contains(output, "write error: sink unavailable") {
		t.Fatalf("error output = %q", output)
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
		t.Fatalf("line count = %d, want %d", len(lines), goroutines*writes)
	}
	seen := make(map[[2]int]struct{}, goroutines*writes)
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if entry["message"] != "concurrent" || entry["level"] != "INFO" {
			t.Fatalf("unexpected concurrent entry contract: %#v", entry)
		}
		workerValue, workerOK := entry["worker"].(float64)
		writeValue, writeOK := entry["write"].(float64)
		worker, write := int(workerValue), int(writeValue)
		if !workerOK || !writeOK || float64(worker) != workerValue || float64(write) != writeValue ||
			worker < 0 || worker >= goroutines || write < 0 || write >= writes {
			t.Fatalf("invalid worker/write pair: %#v", entry)
		}
		pair := [2]int{worker, write}
		if _, duplicate := seen[pair]; duplicate {
			t.Fatalf("duplicate worker/write pair %v", pair)
		}
		seen[pair] = struct{}{}
	}
	if len(seen) != goroutines*writes {
		t.Fatalf("unique worker/write pairs = %d, want %d", len(seen), goroutines*writes)
	}
}

func TestLoggerWithoutRequestMetadataIsNoop(t *testing.T) {
	t.Parallel()
	contexts := []struct {
		name string
		ctx  context.Context
	}{
		{name: "nil"},
		{name: "background", ctx: context.Background()},
	}
	for _, tt := range contexts {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			logger := Logger(tt.ctx)
			if logger == nil {
				t.Fatal("Logger returned nil without request metadata")
			}
			if logger.Core().Enabled(zap.InfoLevel) || logger.Check(zap.InfoLevel, "must be discarded") != nil {
				t.Fatal("Logger without request metadata was not a no-op")
			}
		})
	}
}

func TestLoggerReturnsInstalledRequestLogger(t *testing.T) {
	t.Parallel()
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
	entry := decodeSingleLogLine(t, buffer.String())
	if entry["message"] != "handler" || entry["request_id"] != "req-1" {
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
