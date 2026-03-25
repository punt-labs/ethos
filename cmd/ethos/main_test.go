package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, oneLine(tt.input))
		})
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Mal Reynolds", "mal-reynolds"},
		{"Alice", "alice"},
		{"Bob O'Brien", "bob-obrien"},
		{"test 123", "test-123"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, slugify(tt.input))
		})
	}
}

// captureStdout runs fn while capturing os.Stdout and returns the output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

func TestVersionCommand(t *testing.T) {
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })
	rootCmd.SetArgs([]string{"version"})
	out := captureStdout(t, func() {
		err := rootCmd.Execute()
		require.NoError(t, err)
	})
	assert.Contains(t, out, version)
}

func TestVersionCommandJSON(t *testing.T) {
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })
	rootCmd.SetArgs([]string{"version", "--json"})
	out := captureStdout(t, func() {
		err := rootCmd.Execute()
		require.NoError(t, err)
	})
	assert.True(t, jsonOutput, "--json flag should set jsonOutput")
	var parsed map[string]string
	err := json.Unmarshal([]byte(out), &parsed)
	require.NoError(t, err, "output should be valid JSON")
	assert.Equal(t, version, parsed["version"])
}

func TestResolveAgentJSONFlag(t *testing.T) {
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })
	// resolve-agent is a native cobra command; --json is a persistent flag
	// that cobra parses automatically.
	rootCmd.SetArgs([]string{"resolve-agent", "--json"})
	// Execute may fail (no git repo) — that's fine; we only care that
	// jsonOutput was set during flag parsing.
	_ = rootCmd.Execute()
	assert.True(t, jsonOutput, "--json flag should set jsonOutput via persistent flag")
}
