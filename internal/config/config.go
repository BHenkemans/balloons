// Package config holds small helpers for translating process environment
// variables into typed values used by cmd/server. The package is kept
// dependency-free so each helper is trivially testable in isolation.
package config

import (
	"os"
	"strings"
)

// Getenv returns the value of k, or def if k is unset or empty.
func Getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ParseCSV splits a comma-separated env-var value into a slice, trimming
// whitespace around each entry and dropping empties. Returns nil for an
// empty input so callers can range over it without a length check.
func ParseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
