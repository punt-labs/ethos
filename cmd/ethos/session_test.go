package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShortID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"uuid", "abcdefgh-1234-5678-9abc-def012345678", "abcdefgh"},
		{"first segment only", "abcdefgh", "abcdefgh"},
		{"short string", "short", "short"},
		{"eight chars", "12345678", "12345678"},
		{"empty", "", ""},
		{"three chars", "abc", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shortID(tt.input))
		})
	}
}

func TestFormatStarted(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, got string)
	}{
		{
			name:  "valid RFC3339",
			input: "2026-03-29T14:22:00Z",
			check: func(t *testing.T, got string) {
				// Shape is fixed across timezones:
				// YYYY-MM-DD HH:MM ZONE. Year 2026 is absolute;
				// month/day may shift one day either side of the
				// UTC instant depending on local zone. ZONE is
				// either an IANA abbreviation (e.g. PST) or a
				// numeric offset fallback of ±HH, ±HHMM, or
				// ±HHMMSS (e.g. +05, +0530, -0700) when the
				// Location has no named zone abbreviation.
				assert.Regexp(t, `^2026-\d{2}-\d{2} \d{2}:\d{2} ([A-Z]{2,5}|[+-]\d{2}(\d{2}(\d{2})?)?)$`, got)
			},
		},
		{
			name:  "invalid timestamp returns raw",
			input: "not-a-timestamp",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "not-a-timestamp", got)
			},
		},
		{
			name:  "empty string returns raw",
			input: "",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "", got)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStarted(tt.input)
			tt.check(t, got)
		})
	}
}
