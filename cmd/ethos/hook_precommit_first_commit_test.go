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
// pre-commit hook. The untrack-local step must not stage its record-tree
// deletions into that commit: a tracked trace file the user did not touch is
// still in the committed tree afterwards, and the hook neither blocks nor
// stalls. (A force-added file under .punt-labs/local/ is different: the
// pre-commit pass strips it from the committing index by design, so it is
// absent from the tree.)
func TestFirstCommitAfterUpgradeDoesNotCarryRepairDeletions(t *testing.T) {
	se := enabledHookRepo(t)
	// The installed shim resolves `ethos` from PATH; the commit must find
	// the built binary there, whatever the host has installed.
	binDir := filepath.Dir(ethosBinary)
	env := make([]string, 0, len(se.env)+1)
	for _, e := range se.env {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	env = append(env, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	se.env = env
	// A legacy mission event log in the record tree: step 9 stages its
	// deletion on a full repair. A local file force-added alongside: the
	// pre-commit pass strips that one on purpose.
	trace := filepath.Join(".punt-labs", "ethos", "missions", "m-2026-01-01-001", "log.jsonl")
	local := filepath.Join(".punt-labs", "local", "ethos", "stray.txt")
	for _, rel := range []string{trace, local} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(se.repo, rel)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(se.repo, rel), []byte(`{"ts":"2026-01-01T00:00:00Z","event":"legacy"}`+"\n"), 0o600))
	}
	gitRunEnv(t, se.repo, se.env, "add", "-f", trace, local)
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

	require.NoError(t, os.WriteFile(filepath.Join(se.repo, "work.txt"), []byte("the user's change\n"), 0o644))
	gitRunEnv(t, se.repo, se.env, "add", "work.txt")
	cmd := testhelpers.GitCommand("-C", se.repo, "commit", "-q", "-m", "user work")
	cmd.Env = se.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit through the installed hook: %v\n%s", err, out)
	}

	ls := testhelpers.GitCommand("-C", se.repo, "ls-tree", "-r", "--name-only", "HEAD")
	ls.Env = se.env
	out, err := ls.Output()
	require.NoError(t, err)
	tree := string(out)
	assert.Contains(t, tree, "work.txt")
	assert.True(t, strings.Contains(tree, filepath.ToSlash(trace)),
		"the repair staged a record-tree deletion into the user's commit; tree:\n%s", tree)
	assert.False(t, strings.Contains(tree, filepath.ToSlash(local)),
		"the pre-commit pass strips a force-added local file from the commit; tree:\n%s", tree)
}
