package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestLiveApprovalInteraction(t *testing.T) {
	require.ErrorContains(t, checkLiveApprovalTerminal(false, nil), "interactive TTY")
	for _, marker := range []string{"CLAUDECODE=1", "OPENCODE=1", "OPENCODE_CLIENT=cli"} {
		require.ErrorContains(t, checkLiveApprovalTerminal(true, []string{marker}), "agent environment")
	}
	require.NoError(t, checkLiveApprovalTerminal(true, []string{"PATH=/usr/bin"}))
	require.Error(t, confirmLiveApproval(strings.NewReader("n\n")))
	require.Error(t, confirmLiveApproval(strings.NewReader("yes")))
	require.NoError(t, confirmLiveApproval(strings.NewReader("yes\n")))
}

func TestHandoffApprovalConfirmationAndRevocation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	for name, body := range map[string]string{
		".punt-labs/ethos.yaml":                          "checks: project\n",
		".punt-labs/ethos/checks/project.yaml":           "name: project\nchecks:\n  - {name: live, stage: handoff, files: all, run: [/usr/bin/true]}\n",
		".punt-labs/ethos/checks/snapshots/project.yaml": "checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n",
	} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	previous := cmdRoots.Swap(&repoRoots{Located: resolve.Located{Checkout: root}})
	t.Cleanup(func() { cmdRoots.Store(previous) })

	var output bytes.Buffer
	approve := &cobra.Command{}
	approve.SetOut(&output)
	require.ErrorContains(t, confirmAndApproveLiveHandoff(approve, "project", strings.NewReader("no\n")), "live policy not approved")
	require.NotContains(t, output.String(), "Approved project")
	output.Reset()
	require.NoError(t, confirmAndApproveLiveHandoff(approve, "project", strings.NewReader("yes\n")))
	require.Contains(t, output.String(), "Policy SHA-256:")
	require.Contains(t, output.String(), "Approved project")

	revoke, args, err := rootCmd.Find([]string{"handoff", "revoke-live", "project"})
	require.NoError(t, err)
	require.NoError(t, revoke.RunE(revoke, args))
}
