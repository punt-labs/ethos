package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShortID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abcdefgh-1234-5678-9abc-def012345678", "abcdefgh"},
		{"abcdefgh", "abcdefgh"},
		{"short", "short"},
		{"12345678", "12345678"},
		{"", ""},
		{"abc", "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
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
				// Should contain day-of-week, month, day, and time.
				assert.Contains(t, got, "Mar")
				assert.Contains(t, got, "29")
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
