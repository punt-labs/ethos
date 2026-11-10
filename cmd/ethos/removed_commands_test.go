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

// runEthosStderr runs the built binary and returns stderr and the exit code.
func runEthosStderr(t *testing.T, args ...string) (string, int) {
	t.Helper()
	se := setupCLISubprocessEnv(t)
	cmd := exec.Command(ethosBinary, args...)
	cmd.Dir = se.repo
	cmd.Env = withForcedNotUnderClaudeCode(se.env)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stderr.String(), 0
	}
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	return stderr.String(), ee.ExitCode()
}

// TestRemovedCommandsFailInTheBinary runs every removed command through the
// real binary, bare, with an argument and with --help. A script that calls a
// removed command must see a usage error, not the parent group's help and
// exit 0.
func TestRemovedCommandsFailInTheBinary(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the CLI binary must build; this test must not skip")
	for _, removed := range removedCommands {
		for _, extra := range [][]string{nil, {"x"}, {"--help"}} {
			args := append(append([]string{}, removed...), extra...)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				stderr, code := runEthosStderr(t, args...)
				assert.Equal(t, 2, code, "stderr: %s", stderr)
				assert.True(t, strings.Contains(stderr, "unknown command") || strings.Contains(stderr, "unknown flag"),
					"stderr must name an unknown command or flag: %q", stderr)
			})
		}
	}
}

// TestRemovedCommandsFailWithFlagsAndHelpInFront places --json and the help
// word before or inside each removed command. Cobra reads those as a request
// for the parent's help, so each must still be a usage error.
func TestRemovedCommandsFailWithFlagsAndHelpInFront(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the CLI binary must build; this test must not skip")
	var cases [][]string
	for _, removed := range removedCommands {
		cases = append(cases,
			append([]string{"--json"}, removed...),
			append([]string{removed[0], "--json"}, removed[1:]...),
			append([]string{"help"}, removed...),
			append([]string{removed[0], "--help"}, removed[1:]...),
			append([]string{removed[0], "-h"}, removed[1:]...),
			append([]string{"--json", removed[0], "--help"}, removed[1:]...),
		)
	}
	cases = append(cases, []string{"identity", "--json", "x"}, []string{"help", "repair"})
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stderr, code := runEthosStderr(t, args...)
			assert.Equal(t, 2, code, "stderr: %s", stderr)
			assert.True(t, strings.Contains(stderr, "unknown command") || strings.Contains(stderr, "unknown flag"),
				"stderr must name an unknown command or flag: %q", stderr)
		})
	}
}

func TestRemovedVendorFlagsFailInTheBinary(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "the CLI binary must build; this test must not skip")
	for _, args := range [][]string{{"vendor", "--no-teams"}, {"vendor", "--from", "x"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stderr, code := runEthosStderr(t, args...)
			assert.Equal(t, 2, code, "stderr: %s", stderr)
			assert.Contains(t, stderr, "unknown flag")
		})
	}
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
