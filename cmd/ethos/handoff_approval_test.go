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

func TestHandoffApprovalSelectsAgentOverride(t *testing.T) {
	for _, override := range []string{"identity", "role"} {
		t.Run(override, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			runGit(t, root, "init", "-q")
			identity := "name: Worker\nhandle: worker\nkind: agent\n"
			role := "name: writer\ntools: [Edit]\n"
			if override == "identity" {
				identity += "checks: override\n"
			} else {
				role += "checks: override\n"
			}
			for name, body := range map[string]string{
				".punt-labs/ethos.yaml":                           "team: crew\nchecks: none\n",
				".punt-labs/ethos/identities/worker.yaml":         identity,
				".punt-labs/ethos/roles/writer.yaml":              role,
				".punt-labs/ethos/teams/crew.yaml":                "name: crew\nmembers: [{identity: worker, role: writer}]\n",
				".punt-labs/ethos/checks/override.yaml":           "name: override\nchecks:\n  - {name: live, stage: handoff, files: all, run: [/usr/bin/true]}\n",
				".punt-labs/ethos/checks/snapshots/override.yaml": "checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n",
			} {
				path := filepath.Join(root, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			}
			previous := cmdRoots.Swap(&repoRoots{Located: resolve.Located{Checkout: root}})
			t.Cleanup(func() { cmdRoots.Store(previous) })
			approve, _, err := rootCmd.Find([]string{"handoff", "approve-live"})
			require.NoError(t, err)
			var output bytes.Buffer
			approve.SetOut(&output)
			t.Cleanup(func() { approve.SetOut(nil) })
			require.NoError(t, approve.ParseFlags([]string{"--agent", "worker"}))
			t.Cleanup(func() {
				require.NoError(t, approve.Flags().Set("agent", ""))
				approve.Flags().Lookup("agent").Changed = false
			})
			require.NoError(t, confirmAndApproveLiveHandoff(approve, "override", strings.NewReader("yes\n")))
			require.Contains(t, output.String(), "Approved override")
		})
	}
}
