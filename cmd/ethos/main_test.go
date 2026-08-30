package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
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

// captureStdoutE is like captureStdout but for functions that return error.
// The error is checked with require.NoError so test failures are reported
// at the call site rather than silently swallowed.
func captureStdoutE(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	require.NoError(t, fn())

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
	// Compare against resolveVersion(), not the raw `version` var: `version`
	// is empty unless the Makefile's ldflags set it (main.go, ethos-vqbk),
	// and `assert.Contains(out, "")` would pass vacuously and prove nothing.
	assert.Contains(t, out, resolveVersion())
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
	assert.Equal(t, resolveVersion(), parsed["version"])
}

// fakeBuildInfo swaps readBuildInfo for the duration of the test so
// resolveVersion's fallback branch can be exercised deterministically —
// the real debug.ReadBuildInfo() result depends on the Go toolchain
// version and on whether the checkout has VCS metadata (see ethos-vqbk:
// on Go 1.24+, a real git checkout stamps a git-describe-derived version
// instead of the "(devel)" sentinel older toolchains used).
func fakeBuildInfo(t *testing.T, info *debug.BuildInfo, ok bool) {
	t.Helper()
	old := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return info, ok }
	t.Cleanup(func() { readBuildInfo = old })
}

// TestResolveVersion_LdflagsSet asserts the ldflags-injected version wins
// outright, even when it looks nothing like a build-info module version —
// resolveVersion must never second-guess what the Makefile computed.
func TestResolveVersion_LdflagsSet(t *testing.T) {
	old := version
	version = "v9.9.9-custom"
	t.Cleanup(func() { version = old })

	assert.Equal(t, "v9.9.9-custom", resolveVersion())
}

// TestResolveVersion_BuildInfoAbsent covers the case where ldflags did not
// run and runtime/debug has nothing to offer either (ok == false) —
// resolveVersion must still return something, never empty.
func TestResolveVersion_BuildInfoAbsent(t *testing.T) {
	old := version
	version = ""
	t.Cleanup(func() { version = old })
	fakeBuildInfo(t, nil, false)

	assert.Equal(t, "dev", resolveVersion())
}

// TestResolveVersion_GoInstallStampsRealVersion is the case ethos-vqbk
// exists to fix: `go install github.com/punt-labs/ethos/v4/cmd/ethos@v4.16.0`
// compiles source directly, so ldflags never runs, but Go always stamps
// the requested module version into info.Main.Version. resolveVersion
// must surface that real version instead of falling through to "dev".
func TestResolveVersion_GoInstallStampsRealVersion(t *testing.T) {
	old := version
	version = ""
	t.Cleanup(func() { version = old })
	fakeBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/punt-labs/ethos/v4",
			Version: "v4.16.0",
		},
	}, true)

	assert.Equal(t, "v4.16.0", resolveVersion())
}

// TestResolveVersion_DevelNormalizedToDev pins the documented mapping from
// the runtime/debug sentinel "(devel)" — a bare `go build` with no VCS
// metadata to derive a version from — to this command's own "dev"
// convention.
func TestResolveVersion_DevelNormalizedToDev(t *testing.T) {
	old := version
	version = ""
	t.Cleanup(func() { version = old })
	fakeBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/punt-labs/ethos/v4",
			Version: "(devel)",
		},
	}, true)

	assert.Equal(t, "dev", resolveVersion())
}

// TestResolveVersion_BareGoBuildStampsDirtyVersion covers the Go 1.24+
// case discovered while fixing ethos-vqbk: building from inside a real
// VCS checkout stamps a git-describe-derived version, with a "+dirty"
// suffix when the working tree has uncommitted changes, rather than the
// older "(devel)" sentinel. resolveVersion surfaces that as-is — it is
// genuinely more informative than "dev".
func TestResolveVersion_BareGoBuildStampsDirtyVersion(t *testing.T) {
	old := version
	version = ""
	t.Cleanup(func() { version = old })
	fakeBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{
			Path:    "github.com/punt-labs/ethos/v4",
			Version: "v4.16.0+dirty",
		},
	}, true)

	assert.Equal(t, "v4.16.0+dirty", resolveVersion())
}

// TestExitCode2_BadFlag runs the compiled binary with an unknown flag
// and asserts that the process exits with code 2. Subprocess test
// because os.Exit cannot be captured in-process.
func TestExitCode2_BadFlag(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}
	cmd := exec.Command(ethosBinary, "--nonexistent-flag")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected non-zero exit")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T", err)
	assert.Equal(t, 2, exitErr.ExitCode(), "bad flag should exit 2; output: %s", out)
}

// TestExitCode2_UnknownCommand runs the binary with a nonexistent
// subcommand and asserts exit code 2.
func TestExitCode2_UnknownCommand(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}
	cmd := exec.Command(ethosBinary, "nonexistent-command")
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected non-zero exit")
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, "expected *exec.ExitError, got %T", err)
	assert.Equal(t, 2, exitErr.ExitCode(), "unknown command should exit 2; output: %s", out)
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
