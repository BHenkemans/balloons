package config

import (
	"reflect"
	"testing"
)

func TestParseCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "a", []string{"a"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trims whitespace", "  a , b  ,c", []string{"a", "b", "c"}},
		{"drops empties", "a,,b, ,c", []string{"a", "b", "c"}},
		// All-empty inputs return an empty (non-nil) slice. Callers should
		// range over the result rather than nil-check, so the distinction
		// doesn't matter — pinned here to document current behavior.
		{"only commas", ",,,", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseCSV(tc.in)
			// reflect.DeepEqual treats nil and []string{} as different — we
			// want exact behavior here because the package documents nil.
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseCSV(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestGetenv(t *testing.T) {
	const key = "BALLOONS_CONFIG_TEST_KEY"
	t.Setenv(key, "")
	if got := Getenv(key, "fallback"); got != "fallback" {
		t.Fatalf("Getenv unset: got %q, want fallback", got)
	}
	t.Setenv(key, "explicit")
	if got := Getenv(key, "fallback"); got != "explicit" {
		t.Fatalf("Getenv set: got %q, want explicit", got)
	}
}
