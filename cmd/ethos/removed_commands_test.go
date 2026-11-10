//go:build linux || darwin

package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// removedCommands are the commands DES-084 deletes: ethos repairs its own
// storage, so the user never runs a repair, migrate or purge command.
var removedCommands = [][]string{
	{"repair"},
	{"audit", "quarantine"},
	{"audit", "migrate"},
	{"mission", "scrub"},
	{"mission", "migrate"},
	{"adr", "migrate"},
	{"session", "purge"},
	{"show"},
	{"list"},
	{"resolve-agent"},
	{"iam"},
	{"create"},
	{"ext", "del"},
	{"session", "create"},
	{"session", "delete"},
	{"session", "write-current"},
	{"session", "delete-current"},
	{"session", "roster"},
}

// keptCommands survive the removal on purpose.
var keptCommands = [][]string{
	{"identity", "get"},
	{"hook", "commit-trailers"},
	{"hook", "commit-message"},
	{"audit", "seal"},
	{"session", "iam"},
}

func TestRemovedCommandsAreNotInTheTree(t *testing.T) {
	for _, args := range removedCommands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, rest, err := rootCmd.Find(args)
			if err == nil && len(rest) == 0 && c != rootCmd {
				t.Fatalf("%q is still a command (%s)", strings.Join(args, " "), c.CommandPath())
			}
		})
	}
}

func TestRemovedCommandsFailInTheBinary(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	for _, args := range [][]string{{"repair"}, {"resolve-agent"}, {"show", "x"}, {"list"}, {"create"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			cmd := exec.Command(ethosBinary, args...)
			cmd.Dir = se.repo
			cmd.Env = withForcedNotUnderClaudeCode(se.env)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, string(out))
			assert.Contains(t, string(out), "unknown command")
		})
	}
}

func TestSessionPurgeIsUnknownInTheBinary(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)
	cmd := exec.Command(ethosBinary, "session", "purge")
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), "unknown command")
}

func TestKeptCommandsRemain(t *testing.T) {
	for _, args := range keptCommands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			c, rest, err := rootCmd.Find(args)
			require.NoError(t, err)
			assert.Empty(t, rest)
			assert.Equal(t, "ethos "+strings.Join(args, " "), c.CommandPath())
		})
	}
}

func TestHiddenKeptCommands(t *testing.T) {
	for _, args := range [][]string{{"hook", "commit-trailers"}, {"hook", "commit-message"}, {"audit", "seal"}} {
		c, _, err := rootCmd.Find(args)
		require.NoError(t, err)
		assert.True(t, c.Hidden, "%v must be hidden from help", args)
	}
}

func TestVendorHasNoPartialSnapshotFlags(t *testing.T) {
	for _, name := range []string{"no-teams", "from"} {
		assert.Nil(t, vendorCmd.Flags().Lookup(name), "--%s is removed", name)
	}
}
