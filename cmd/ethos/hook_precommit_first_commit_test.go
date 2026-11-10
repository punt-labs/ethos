//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// The first commit after an upgrade runs the full repair through the
// pre-commit hook. The untrack-local step must not stage its deletions into
// that commit: a tracked local file the user did not touch is still in the
// committed tree afterwards, and the hook neither blocks nor stalls.
func TestFirstCommitAfterUpgradeDoesNotCarryRepairDeletions(t *testing.T) {
	se := enabledHookRepo(t)
	local := filepath.Join(".punt-labs", "local", "ethos", "stray.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(se.repo, local)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, local), []byte("force-added before the upgrade\n"), 0o600))
	gitRunEnv(t, se.repo, se.env, "add", "-f", local)
	gitRunEnv(t, se.repo, se.env, "commit", "-q", "--no-verify", "-m", "pre-upgrade state")

	// An upgraded binary meets a store with no stamps: every step runs on
	// the next entry, which is this commit's pre-commit hook.
	for _, stamp := range []string{
		autorepair.GlobalStampPath(filepath.Join(se.home, ".punt-labs", "ethos")),
		filepath.Join(se.repo, ".punt-labs", "local", "ethos", autorepair.StampName),
	} {
		if err := os.Remove(stamp); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	commit(t, se.repo, se.env, "work.txt", "the user's change\n", "user work")

	cmd := testhelpers.GitCommand("-C", se.repo, "ls-tree", "-r", "--name-only", "HEAD")
	cmd.Env = se.env
	out, err := cmd.Output()
	require.NoError(t, err)
	tree := string(out)
	assert.Contains(t, tree, "work.txt")
	assert.True(t, strings.Contains(tree, filepath.ToSlash(local)),
		"the repair staged a deletion into the user's commit; tree:\n%s", tree)
}
