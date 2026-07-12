package obs

import (
	"strings"
	"testing"
)

func TestParseTraceparent(t *testing.T) {
	t.Parallel()

	valid := []struct {
		value   string
		sampled bool
	}{
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", true},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00", false},
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09", true},
		{"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra", true},
	}
	for _, tt := range valid {
		trace, ok := ParseTraceparent(tt.value)
		if !ok || !trace.Valid {
			t.Fatalf("ParseTraceparent(%q) rejected a valid value", tt.value)
		}
		if trace.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || trace.ParentID != "00f067aa0ba902b7" {
			t.Fatalf("unexpected IDs: %#v", trace)
		}
		if trace.Sampled != tt.sampled || trace.Traceparent != tt.value {
			t.Fatalf("unexpected trace: %#v", trace)
		}
	}

	base := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	invalid := []string{
		"", base[:len(base)-1], base + "-extra",
		"0A-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4BF92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00F067aa0ba902b7-01",
		"0g-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00_4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736_00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7_01",
		"01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01extra",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g",
		base + strings.Repeat("a", maxTraceparentLen),
	}
	for _, value := range invalid {
		if trace, ok := ParseTraceparent(value); ok {
			t.Fatalf("ParseTraceparent(%q) accepted invalid value: %#v", value, trace)
		}
	}
}

func FuzzParseTraceparent(f *testing.F) {
	f.Add("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
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
	})
}
