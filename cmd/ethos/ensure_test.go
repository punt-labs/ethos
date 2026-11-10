package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// fakeEnsure replaces ensureStorage for one test and returns the call count.
func fakeEnsure(t *testing.T, res autorepair.Result) *int {
	t.Helper()
	old := ensureStorage
	n := 0
	ensureStorage = func(context.Context, resolve.Located, string) (autorepair.Result, error) {
		n++
		return res, nil
	}
	t.Cleanup(func() { ensureStorage = old; repairNotice.Store(nil) })
	return &n
}

// topLevel is the name of the command directly under the root that cmd is, or
// is below.
func topLevel(cmd *cobra.Command) string {
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	return cmd.Name()
}

// Every command passes the repair gate except the four that never read
// storage. The walk is over the cobra tree, so a command added later is
// covered or this test fails (DES-084 Wiring). Two properties per leaf: the
// gate decides to run or not for the command (through the root hook), and
// the hook that cobra actually runs for the leaf is root's — a subcommand
// that sets its own PersistentPreRun(E) shadows root's, and with it the gate,
// unless cobra is told to traverse. Without the second check the first is
// hollow, because it calls establishRoots by hand.
func TestEveryCommandPassesEnsure(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	n := fakeEnsure(t, autorepair.Result{})
	exempt := map[string]bool{"version": true, "completion": true, "help": true, "uninstall": true}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c != rootCmd {
			before := *n
			c.SetErr(&bytes.Buffer{})
			// A refusal is allowed; reaching the gate first is the property.
			_ = establishRoots(c, nil)
			called := *n > before
			if exempt[topLevel(c)] {
				assert.False(t, called, "%s must not pass Ensure", c.CommandPath())
			} else {
				assert.True(t, called, "%s does not pass Ensure", c.CommandPath())
			}
			cmdRoots.Store(nil)
			assertRootHookIsNearest(t, c)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// assertRootHookIsNearest fails when a command between c and the root
// (c included) sets a PersistentPreRun or PersistentPreRunE: cobra runs only
// the nearest one unless EnableTraverseRunHooks is set, so that command would
// skip the repair gate.
func assertRootHookIsNearest(t *testing.T, c *cobra.Command) {
	t.Helper()
	assert.Empty(t, shadowingHooks(c), "these commands shadow root's repair gate")
	assert.NotNil(t, rootCmd.PersistentPreRunE, "root has no PersistentPreRunE")
}

// shadowingHooks names the commands from c up to (not including) the root
// that set a persistent pre-run hook of their own.
func shadowingHooks(c *cobra.Command) []string {
	var out []string
	for cur := c; cur != nil && cur != rootCmd; cur = cur.Parent() {
		if cur.PersistentPreRun != nil || cur.PersistentPreRunE != nil {
			out = append(out, cur.CommandPath())
		}
	}
	return out
}

// The guard above catches a shadowing hook: a scratch subtree whose middle
// node sets PersistentPreRunE is reported, a clean one is not.
func TestRootHookGuardDetectsShadowing(t *testing.T) {
	mid := &cobra.Command{Use: "mid", PersistentPreRunE: func(*cobra.Command, []string) error { return nil }}
	leaf := &cobra.Command{Use: "leaf", Run: func(*cobra.Command, []string) {}}
	mid.AddCommand(leaf)
	rootCmd.AddCommand(mid)
	t.Cleanup(func() { rootCmd.RemoveCommand(mid) })

	assert.Equal(t, []string{"ethos mid"}, shadowingHooks(leaf), "a shadowing PersistentPreRunE is reported")
	mid.PersistentPreRunE = nil
	assert.Empty(t, shadowingHooks(leaf), "a clean chain passes")
}

// A real execution of a leaf reaches the gate exactly once: the hook cobra
// runs for the leaf is root's.
func TestExecutedCommandReachesEnsureOnce(t *testing.T) {
	n := fakeEnsure(t, autorepair.Result{})
	_, _, err := execHandler(t, "doctor", "--json")
	require.NoError(t, err)
	assert.Equal(t, 1, *n, "doctor passed Ensure %d times", *n)
}

func TestEnsureLineGoesToStderr(t *testing.T) {
	const line = "ethos: upgraded storage (3 items; see ~/.punt-labs/ethos/logs/repair.log)"
	fakeEnsure(t, autorepair.Result{Items: 3, Repaired: true})
	c, _, err := rootCmd.Find([]string{"doctor"})
	require.NoError(t, err)
	var errOut bytes.Buffer
	c.SetErr(&errOut)
	require.NoError(t, establishRoots(c, nil))
	assert.Equal(t, line+"\n", errOut.String())
	assert.Nil(t, repairNotice.Load(), "the line is printed once, not also held for a hook")
}

// Session start prints JSON on stdout and its stderr goes to an unread log, so
// its line is held for the JSON the hook builds.
func TestEnsureLineForSessionStartIsHeldNotPrinted(t *testing.T) {
	fakeEnsure(t, autorepair.Result{Items: 3, Repaired: true})
	c, _, err := rootCmd.Find([]string{"hook", "session-start"})
	require.NoError(t, err)
	var errOut bytes.Buffer
	c.SetErr(&errOut)
	require.NoError(t, establishRoots(c, nil))
	assert.Empty(t, errOut.String())
	assert.Equal(t, 1, strings.Count(takeRepairNotice(), "upgraded storage"))
	assert.Equal(t, "", takeRepairNotice(), "the notice is taken once")
}

func TestEnsureSilentWhenNothingRepaired(t *testing.T) {
	fakeEnsure(t, autorepair.Result{Residuals: []autorepair.Residual{{Step: "s", ID: "i", Reason: "r"}}})
	c, _, err := rootCmd.Find([]string{"doctor"})
	require.NoError(t, err)
	var errOut bytes.Buffer
	c.SetErr(&errOut)
	require.NoError(t, establishRoots(c, nil))
	assert.Empty(t, errOut.String(), "a residual never produces a line")
}
