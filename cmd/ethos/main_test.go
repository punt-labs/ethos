package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOneLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Direct. Short sentences.", "Direct. Short sentences."},
		{"Line one.\nLine two.", "Line one. Line two."},
		{"  spaces  and\ttabs  ", "spaces and tabs"},
		{"", ""},
		{"   ", ""},
		{"\n\n\n", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, oneLine(tt.input))
	}
}

func TestHasHelpFlag(t *testing.T) {
	assert.True(t, hasHelpFlag([]string{"--help"}))
	assert.True(t, hasHelpFlag([]string{"-h"}))
	assert.True(t, hasHelpFlag([]string{"foo", "--help"}))
	assert.False(t, hasHelpFlag([]string{}))
	assert.False(t, hasHelpFlag([]string{"foo", "bar"}))
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Jim Freeman", "jim-freeman"},
		{"Alice", "alice"},
		{"Bob O'Brien", "bob-obrien"},
		{"test 123", "test-123"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, slugify(tt.input))
	}
}
