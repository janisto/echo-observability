package obs

import (
	"strconv"
	"strings"
	"testing"
)

func TestParseTraceparentAcceptsValidBaseAndFutureVersions(t *testing.T) {
	t.Parallel()

	const futureBase = "fe-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	multibyteAtLimit := "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-" + strings.Repeat("é", 228)
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
		{name: "UTF-8 byte limit", value: multibyteAtLimit, sampled: true},
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
				Level:       TraceContextLevel1,
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
	multibyteOverLimit := "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-" + strings.Repeat("é", 228) + "x"

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
		{name: "over UTF-8 byte limit", value: multibyteOverLimit},
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

func TestTraceContextLevelResolutionAndRandomFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		requested TraceContextLevel
		want      TraceContextLevel
		wantErr   string
	}{
		{name: "default", want: TraceContextLevel1},
		{name: "level 1", requested: TraceContextLevel1, want: TraceContextLevel1},
		{name: "level 2", requested: TraceContextLevel2, want: TraceContextLevel2},
		{
			name:      "unsupported",
			requested: 3,
			wantErr:   "unsupported trace context level 3: supported levels are 1 and 2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveTraceContextLevel(tt.requested)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || got != 0 {
					t.Fatalf(
						"ResolveTraceContextLevel(%d) = (%d, %v), want (0, %q)",
						tt.requested,
						got,
						err,
						tt.wantErr,
					)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ResolveTraceContextLevel(%d) = (%d, %v), want (%d, nil)", tt.requested, got, err, tt.want)
			}
		})
	}

	const prefix = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-"
	flags := []struct {
		value   string
		sampled bool
		random  bool
	}{
		{value: "00"},
		{value: "01", sampled: true},
		{value: "02", random: true},
		{value: "03", sampled: true, random: true},
		{value: "04"},
	}
	for _, tt := range flags {
		trace, ok := ParseTraceparentWithLevel(prefix+tt.value, TraceContextLevel2)
		if !ok || trace.Level != TraceContextLevel2 ||
			trace.Flags != tt.value || trace.Sampled != tt.sampled || trace.Random != tt.random {
			t.Fatalf("Level 2 flags %q parsed as %#v", tt.value, trace)
		}
	}
	level1, ok := ParseTraceparentWithLevel(prefix+"03", TraceContextLevel1)
	if !ok || level1.Level != TraceContextLevel1 || !level1.Sampled || level1.Random {
		t.Fatalf("Level 1 flags parsed as %#v", level1)
	}
	if _, ok := ParseTraceparentWithLevel(prefix+"03", 3); ok {
		t.Fatal("unsupported trace context level parsed a traceparent")
	}
}

func TestParseTracestateLevel1Matrix(t *testing.T) {
	t.Parallel()
	valid512 := "a=" + strings.Repeat("v", 256) + ",b=" + strings.Repeat("w", 251)
	tests := []struct {
		name      string
		rawValues []string
		want      string
		valid     bool
	}{
		{name: "missing", valid: true},
		{name: "empty field", rawValues: []string{""}, valid: true},
		{
			name:      "split wire order",
			rawValues: []string{"vendor1=value1", "vendor2=value2"},
			want:      "vendor1=value1,vendor2=value2",
			valid:     true,
		},
		{
			name:      "optional whitespace",
			rawValues: []string{"  vendor1=value1  ", "\tvendor2=value2\t"},
			want:      "vendor1=value1,vendor2=value2",
			valid:     true,
		},
		{
			name:      "separator whitespace",
			rawValues: []string{"vendor1=value1 \t, \tother= value2\t"},
			want:      "vendor1=value1,other= value2",
			valid:     true,
		},
		{name: "space before equals", rawValues: []string{"vendor =value"}},
		{name: "empty key", rawValues: []string{"=value"}},
		{name: "tab inside value", rawValues: []string{"vendor=\tvalue"}},
		{name: "raw over limit", rawValues: []string{strings.Repeat(" ", 513)}},
		{name: "duplicate key", rawValues: []string{"vendor=value1,vendor=value2"}},
		{
			name:      "empty member",
			rawValues: []string{"vendor=value1,,other=value2"},
			want:      "vendor=value1,,other=value2",
			valid:     true,
		},
		{name: "uppercase key", rawValues: []string{"UPPER=value"}},
		{name: "lower alpha start", rawValues: []string{"a=value,z=value"}, want: "a=value,z=value", valid: true},
		{name: "before lower alpha start", rawValues: []string{"`=value"}},
		{name: "after lower alpha start", rawValues: []string{"{=value"}},
		{
			name:      "maximum simple key",
			rawValues: []string{"a" + strings.Repeat("b", 255) + "=value"},
			want:      "a" + strings.Repeat("b", 255) + "=value",
			valid:     true,
		},
		{name: "overlong simple key", rawValues: []string{"a" + strings.Repeat("b", 256) + "=value"}},
		{name: "multi tenant", rawValues: []string{"tenant@system=value"}, want: "tenant@system=value", valid: true},
		{
			name:      "maximum tenant",
			rawValues: []string{"1" + strings.Repeat("a", 240) + "@system=value"},
			want:      "1" + strings.Repeat("a", 240) + "@system=value",
			valid:     true,
		},
		{name: "overlong tenant", rawValues: []string{"1" + strings.Repeat("a", 241) + "@system=value"}},
		{
			name:      "maximum system",
			rawValues: []string{"tenant@s" + strings.Repeat("a", 13) + "=value"},
			want:      "tenant@s" + strings.Repeat("a", 13) + "=value",
			valid:     true,
		},
		{name: "overlong system", rawValues: []string{"tenant@s" + strings.Repeat("a", 14) + "=value"}},
		{name: "invalid tenant remainder", rawValues: []string{"a!@system=value"}},
		{name: "invalid system remainder", rawValues: []string{"tenant@s!=value"}},
		{name: "multiple at", rawValues: []string{"tenant@sub@system=value"}},
		{name: "value equals", rawValues: []string{"vendor=value=extra"}},
		{name: "leading space value", rawValues: []string{"vendor= value"}, want: "vendor= value", valid: true},
		{name: "space value cannot end", rawValues: []string{"vendor= "}},
		{name: "value lower ASCII boundary", rawValues: []string{"vendor=\x1f"}},
		{name: "value upper ASCII boundary", rawValues: []string{"vendor=~"}, want: "vendor=~", valid: true},
		{name: "value over ASCII boundary", rawValues: []string{"vendor=\x7f"}},
		{name: "32 members", rawValues: []string{tracestateMembers(32)}, want: tracestateMembers(32), valid: true},
		{name: "33 members", rawValues: []string{tracestateMembers(33)}},
		{name: "512 bytes", rawValues: []string{valid512}, want: valid512, valid: true},
		{name: "513 bytes", rawValues: []string{valid512 + "w"}},
		{name: "empty value", rawValues: []string{"vendor="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseTracestate(tt.rawValues, TraceContextLevel1)
			if got != tt.want || valid != tt.valid {
				t.Fatalf(
					"parseTracestate(%q, Level 1) = (%q, %v), want (%q, %v)",
					tt.rawValues,
					got,
					valid,
					tt.want,
					tt.valid,
				)
			}
		})
	}
}

func TestParseTracestateLevel2Matrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
		valid bool
	}{
		{name: "single character key", value: "1=value", want: "1=value", valid: true},
		{
			name:  "multiple at characters",
			value: "tenant@sub@system=value",
			want:  "tenant@sub@system=value",
			valid: true,
		},
		{name: "at cannot start key", value: "@vendor=value"},
		{name: "uppercase remains invalid", value: "Vendor=value"},
		{
			name:  "separator whitespace",
			value: "vendor=value \t, \t1@two= leading\t",
			want:  "vendor=value,1@two= leading",
			valid: true,
		},
		{name: "duplicate key", value: "vendor=first,vendor=second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, valid := parseTracestate([]string{tt.value}, TraceContextLevel2)
			if got != tt.want || valid != tt.valid {
				t.Fatalf(
					"parseTracestate(%q, Level 2) = (%q, %v), want (%q, %v)",
					tt.value,
					got,
					valid,
					tt.want,
					tt.valid,
				)
			}
		})
	}
}

func tracestateMembers(count int) string {
	members := make([]string, count)
	for index := range count {
		members[index] = "v" + strconv.Itoa(index) + "=x"
	}
	return strings.Join(members, ",")
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
		if trace.Traceparent != value ||
			trace.Level != TraceContextLevel1 ||
			trace.Random ||
			trace.Sampled != (flags&0x01 == 0x01) {
			t.Fatalf("parsed trace does not preserve its input semantics: %#v", trace)
		}
	})
}
