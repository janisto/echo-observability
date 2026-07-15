package obs

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseTraceparentAcceptsValidBaseAndFutureVersions(t *testing.T) {
	t.Parallel()

	const futureBase = "fe-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	valid := []struct {
		name    string
		value   string
		sampled bool
	}{
		{
			name:  "version 00 sampled",
			value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", sampled: true,
		},
		{
			name:  "version 00 unsampled",
			value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
		},
		{
			name:  "sampling bit among future flags",
			value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09", sampled: true,
		},
		{name: "future version without extension", value: futureBase},
		{
			name:  "future version with extension",
			value: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra", sampled: true,
		},
		{name: "maximum accepted length", value: maxLengthFutureTraceparent(t)},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trace, ok := ParseTraceparent(tt.value)
			if !ok {
				t.Fatalf("ParseTraceparent(%q) rejected a valid value", tt.value)
			}
			want := TraceContext{
				Traceparent: tt.value,
				TraceID:     "4bf92f3577b34da6a3ce929d0e0e4736",
				ParentID:    "00f067aa0ba902b7",
				Flags:       tt.value[53:55],
				Sampled:     tt.sampled,
				Valid:       true,
			}
			if trace != want {
				t.Fatalf("ParseTraceparent(%q) = %#v, want %#v", tt.value, trace, want)
			}
		})
	}
}

func TestParseTraceparentRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	const base = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	maxLengthFuture := maxLengthFutureTraceparent(t)

	invalid := []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "truncated", value: base[:len(base)-1]},
		{name: "version 00 extension", value: base + "-extra"},
		{name: "uppercase version", value: "0A-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "uppercase trace ID", value: "00-4BF92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "uppercase parent ID", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00F067aa0ba902b7-01"},
		{name: "nonhex version", value: "0g-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "reserved version", value: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "zero trace ID", value: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
		{name: "zero parent ID", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
		{name: "version separator", value: "00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
		{name: "trace separator", value: "00-4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7-01"},
		{name: "parent separator", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7_01"},
		{
			name:  "future extension without separator",
			value: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01extra",
		},
		{name: "nonhex flags", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g"},
		{name: "over maximum length", value: maxLengthFuture + "a"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if trace, ok := ParseTraceparent(tt.value); ok {
				t.Fatalf("ParseTraceparent(%q) accepted invalid value: %#v", tt.value, trace)
			}
		})
	}
}

func maxLengthFutureTraceparent(t *testing.T) string {
	t.Helper()
	const futureBase = "fe-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	value := futureBase + "-" + strings.Repeat("a", maxTraceparentLen-traceparentLen-1)
	if len(value) != maxTraceparentLen {
		t.Fatalf("test fixture length = %d, want %d", len(value), maxTraceparentLen)
	}
	return value
}

func FuzzParseTraceparent(f *testing.F) {
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	f.Add("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00-extra")
	f.Add("")
	f.Add("not-a-traceparent")
	f.Fuzz(func(t *testing.T, value string) {
		trace, ok := ParseTraceparent(value)
		if !ok {
			return
		}
		if !trace.Valid || len(trace.TraceID) != 32 || len(trace.ParentID) != 16 || len(trace.Flags) != 2 {
			t.Fatalf("accepted malformed trace: %#v", trace)
		}
		flags, err := strconv.ParseUint(trace.Flags, 16, 8)
		if err != nil {
			t.Fatalf("accepted invalid flags %q: %v", trace.Flags, err)
		}
		if trace.Traceparent != value || trace.Sampled != (flags&0x01 == 0x01) {
			t.Fatalf("parsed trace does not preserve its input semantics: %#v", trace)
		}
	})
}
