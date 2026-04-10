package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
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
	defer func() { os.Stdout = old }()

	fn()

	w.Close()
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

// TestResolveAgentJSONFlag asserts that resolve-agent --json sets the
// JSON output mode. The command calls os.Exit(1) when it cannot find
// a git repo, which would kill the test process. The subprocess
// pattern avoids that: spawn the real binary, let it exit however it
// wants, and inspect the output.
func TestResolveAgentJSONFlag(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	// Run in a temp dir with no git repo. The command will fail, but
	// if --json is wired correctly the error output will be plain text
	// on stderr (the JSON flag controls stdout formatting, not error
	// reporting). The key assertion: the process does not panic, and
	// running with --json does not produce a cobra parse error.
	cmd := exec.Command(ethosBinary, "resolve-agent", "--json")
	cmd.Dir = t.TempDir()
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	// resolve-agent exits 1 when no repo is found — expected.
	// A non-ExitError (e.g., signal, panic) would be a real failure.
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			assert.Equal(t, 1, exitErr.ExitCode(),
				"resolve-agent should exit 1 on missing repo, not crash")
		} else {
			t.Fatalf("resolve-agent failed unexpectedly: %v", err)
		}
	}

	// Cobra must not complain about unknown flags.
	assert.NotContains(t, errBuf.String(), "unknown flag",
		"--json must be accepted as a persistent flag")
}
