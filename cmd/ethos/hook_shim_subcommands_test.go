//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enabledHookRepo returns a scratch repository with ethos enabled.
func enabledHookRepo(t *testing.T) *cliSubprocessEnv {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "the current CLI must build")
	se := enableSubprocessRepo(t)
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	return se
}

func TestHookPreCommitExitCodes(t *testing.T) {
	t.Run("enabled, nothing pending", func(t *testing.T) {
		se := enabledHookRepo(t)
		stdout, stderr, code := runCLI(t, se, "hook", "pre-commit")
		assert.Zero(t, code, stderr)
		assert.Empty(t, stdout)
	})
	t.Run("seals pending live lines", func(t *testing.T) {
		se := enabledHookRepo(t)
		live := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "sessions", "s1.audit.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
		require.NoError(t, os.WriteFile(live,
			[]byte(`{"ts":"2026-07-22T00:00:01.000000001Z","event":"test","seq":1}`+"\n"), 0o600))
		_, stderr, code := runCLI(t, se, "hook", "pre-commit")
		assert.Zero(t, code, stderr)
		sealed, err := filepath.Glob(filepath.Join(se.repo, ".punt-labs", "local", "ethos", "trace", "sessions", "*", "audit-*.jsonl"))
		require.NoError(t, err)
		assert.Len(t, sealed, 1)
	})
	t.Run("invalid configuration blocks with exit 2", func(t *testing.T) {
		se := enabledHookRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
			[]byte("record: [invalid\n"), 0o644))
		_, stderr, code := runCLI(t, se, "hook", "pre-commit")
		assert.Equal(t, 2, code, stderr)
		assert.Contains(t, stderr, "ethos.yaml")
	})
	t.Run("outside a repository is exit 2", func(t *testing.T) {
		se := freshCLIEnv(t, "tester")
		// TMPDIR is inside this checkout, so t.TempDir() is not outside a repository.
		outside, err := os.MkdirTemp("/tmp", "ethos-hook-outside-*")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(outside) })
		se.repo = outside
		_, stderr, code := runCLI(t, se, "hook", "pre-commit")
		assert.Equal(t, 2, code, stderr)
	})
	t.Run("arguments are a usage error", func(t *testing.T) {
		se := enabledHookRepo(t)
		_, _, code := runCLI(t, se, "hook", "pre-commit", "extra")
		assert.Equal(t, 2, code)
	})
}

func TestHookCommitMsgExitCodes(t *testing.T) {
	msg := func(t *testing.T, se *cliSubprocessEnv) string {
		t.Helper()
		p := filepath.Join(se.home, "MSG")
		require.NoError(t, os.WriteFile(p, []byte("subject\n"), 0o600))
		return p
	}
	t.Run("no mission leaves the message alone", func(t *testing.T) {
		se := enabledHookRepo(t)
		p := msg(t, se)
		stdout, stderr, code := runCLI(t, se, "hook", "commit-msg", p)
		assert.Zero(t, code, stderr)
		assert.Empty(t, stdout)
		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Equal(t, "subject\n", string(got))
	})
	t.Run("environment IDs add trailers", func(t *testing.T) {
		se := enabledHookRepo(t)
		se.env = append(se.env, "MISSION_ID=m-2026-11-04-043", "DELEGATION_ID=d-2026-11-04-043")
		p := msg(t, se)
		stdout, stderr, code := runCLI(t, se, "hook", "commit-msg", p)
		assert.Zero(t, code, stderr)
		assert.Empty(t, stdout)
		got, err := os.ReadFile(p)
		require.NoError(t, err)
		assert.Contains(t, string(got), "Mission: m-2026-11-04-043")
		assert.Contains(t, string(got), "Delegation: d-2026-11-04-043")
	})
	t.Run("an invalid ID is exit 1", func(t *testing.T) {
		se := enabledHookRepo(t)
		se.env = append(se.env, "MISSION_ID=not a mission")
		_, stderr, code := runCLI(t, se, "hook", "commit-msg", msg(t, se))
		assert.Equal(t, 1, code, stderr)
		assert.Contains(t, stderr, "commit-message")
	})
	t.Run("wrong argument count is exit 2", func(t *testing.T) {
		se := enabledHookRepo(t)
		for _, args := range [][]string{{"hook", "commit-msg"}, {"hook", "commit-msg", "a", "b"}} {
			_, _, code := runCLI(t, se, args...)
			assert.Equal(t, 2, code, "%v", args)
		}
	})
}

// The shim no longer clears trace variables around the binary's git calls, so
// a trace aimed at stdout must change neither subcommand's output or status.
func TestHookSubcommandsIgnoreGitTrace(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(msg string) []string
	}{
		{"pre-commit", func(string) []string { return []string{"hook", "pre-commit"} }},
		{"commit-msg", func(m string) []string { return []string{"hook", "commit-msg", m} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := enabledHookRepo(t)
			se.env = append(se.env, "MISSION_ID=m-2026-11-04-043")
			p := filepath.Join(se.home, "MSG")
			require.NoError(t, os.WriteFile(p, []byte("subject\n"), 0o600))
			plainOut, plainErr, plainCode := runCLI(t, se, tc.args(p)...)

			p2 := filepath.Join(se.home, "MSG2")
			require.NoError(t, os.WriteFile(p2, []byte("subject\n"), 0o600))
			se.env = append(se.env, "GIT_TRACE=/dev/stdout", "GIT_TRACE2=/dev/stdout", "GIT_TRACE2_EVENT=/dev/stdout")
			tracedOut, tracedErr, tracedCode := runCLI(t, se, tc.args(p2)...)

			assert.Equal(t, plainCode, tracedCode)
			assert.Equal(t, plainOut, tracedOut)
			assert.Equal(t, plainErr, tracedErr)
			assert.Empty(t, tracedOut)
		})
	}
}
