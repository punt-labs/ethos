package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				// Compute expected in local timezone to avoid day-shift failures.
				ts, err := time.Parse(time.RFC3339, "2026-03-29T14:22:00Z")
				require.NoError(t, err)
				local := ts.Local()
				assert.Contains(t, got, local.Format("Jan"))
				assert.Contains(t, got, fmt.Sprintf("%d", local.Day()))
				assert.Regexp(t, `\d{2}:\d{2}`, got)
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
