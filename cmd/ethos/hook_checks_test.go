//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitChecks(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		code       int
	}{
		{"default blocks", "", 2}, {"advisory continues", "    on_fail: advisory\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			se := enabledHookRepo(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos.yaml"), []byte("checks: project\n"), 0600))
			dir := filepath.Join(se.repo, ".punt-labs/ethos/checks")
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte("name: project\nchecks:\n  - name: commit-probe\n    stage: commit\n    files: all\n    run: [missing-commit-probe]\n"+tc.mode), 0600))
			out, stderr, code := runCLI(t, se, "hook", "pre-commit")
			require.Equal(t, tc.code, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "commit-probe")
		})
	}
	t.Run("broken policy blocks", func(t *testing.T) {
		se := enabledHookRepo(t)
		require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos.yaml"), []byte("checks: absent\n"), 0600))
		_, stderr, code := runCLI(t, se, "hook", "pre-commit")
		require.Equal(t, 2, code, stderr)
		require.Contains(t, stderr, "absent")
	})
}

func TestCommitKilledBeforeSideEffects(t *testing.T) {
	se := enabledHookRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos.yaml"), []byte("checks: project\n"), 0600))
	dir := filepath.Join(se.repo, ".punt-labs/ethos/checks")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte("name: project\nchecks:\n  - name: blocked\n    stage: commit\n    files: all\n    run: [missing-commit-probe]\n"), 0600))
	ignore := filepath.Join(se.repo, ".gitignore")
	require.NoError(t, os.WriteFile(ignore, []byte("# user ignore file\n"), 0600))
	local := ".punt-labs/local/ethos/stray.txt"
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, local), []byte("keep staged\n"), 0600))
	gitRunEnv(t, se.repo, se.env, "add", "-f", local)
	live := filepath.Join(se.repo, ".punt-labs/local/ethos/sessions/s1.audit.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0700))
	require.NoError(t, os.WriteFile(live, []byte(`{"ts":"2026-07-22T00:00:01.000000001Z","event":"test","seq":1}`+"\n"), 0600))
	before := map[string][]byte{}
	for _, path := range []string{ignore, filepath.Join(se.repo, ".git/index"), live} {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		before[path] = data
	}
	require.NoError(t, os.WriteFile(filepath.Join(se.home, ".punt-hooks-kill"), nil, 0600))
	out, stderr, code := runCLI(t, se, "hook", "pre-commit")
	assert.Zero(t, code, stderr)
	assert.Empty(t, out)
	assert.Empty(t, stderr)
	for path, want := range before {
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, want, got, path)
	}
	sealed, err := filepath.Glob(filepath.Join(se.repo, ".punt-labs/local/ethos/trace/sessions/*/audit-*.jsonl"))
	require.NoError(t, err)
	assert.Empty(t, sealed)
}

func TestHooksKilledBeforeRootResolution(t *testing.T) {
	for _, name := range []string{"pre-commit", "subagent-stop"} {
		t.Run(name, func(t *testing.T) {
			se := enabledHookRepo(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.home, ".punt-hooks-kill"), nil, 0600))
			bin := t.TempDir()
			marker := filepath.Join(bin, "called")
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called > '"+marker+"'\nexit 99\n"), 0700))
			se.env = append(se.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, stderr, code := runCLI(t, se, "hook", name)
			assert.Zero(t, code, stderr)
			assert.Empty(t, out)
			assert.Empty(t, stderr)
			assert.NoFileExists(t, marker)
		})
	}
}

func TestCommitSessionChecks(t *testing.T) {
	se := enabledHookRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos.yaml"), []byte("checks: none\n"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(se.repo, ".punt-labs/ethos/identities"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs/ethos/identities/worker.yaml"), []byte("name: Worker\nhandle: worker\nkind: agent\nchecks: project\n"), 0600))
	dir := filepath.Join(se.repo, ".punt-labs/ethos/checks")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte("name: project\nchecks:\n  - name: session-check\n    stage: commit\n    files: all\n    run: [missing-session-probe]\n"), 0600))
	ss := session.NewStore(filepath.Join(se.home, ".punt-labs/ethos"))
	require.NoError(t, ss.Create("commit-checks", session.Participant{AgentID: "human"}, session.Participant{AgentID: "worker-id", Persona: "worker"}, "", ""))
	se.env = append(se.env, "ETHOS_SESSION=commit-checks", "ETHOS_AGENT_ID=worker-id")
	_, stderr, code := runCLI(t, se, "hook", "pre-commit")
	require.Equal(t, 2, code, stderr)
	require.Contains(t, stderr, "session-check")
	require.NoError(t, os.WriteFile(filepath.Join(se.home, ".punt-hooks-kill"), nil, 0600))
	_, stderr, code = runCLI(t, se, "hook", "pre-commit")
	require.Zero(t, code, stderr)
	require.NotContains(t, stderr, "session-check")
}
