package main

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
			got := parseCSV(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseCSV(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestGetenv(t *testing.T) {
	const key = "BALLOONS_CONFIG_TEST_KEY"
	t.Setenv(key, "")
	if got := getenv(key, "fallback"); got != "fallback" {
		t.Fatalf("getenv unset: got %q, want fallback", got)
	}
	t.Setenv(key, "explicit")
	if got := getenv(key, "fallback"); got != "explicit" {
		t.Fatalf("getenv set: got %q, want explicit", got)
	}
}
