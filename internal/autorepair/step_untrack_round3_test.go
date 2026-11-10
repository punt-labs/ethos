package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

const (
	fullBody  = "{\"ts\":\"1\",\"record\":\"full\"}\n"
	blameBody = "{\"ts\":\"1\",\"record\":\"blame\"}\n"
)

func legacyChunk(sessionDir string) string {
	return ".punt-labs/ethos/sessions/" + sessionDir + "/audit-" + audit.TSToField(1) + "-" + audit.TSToField(2) + ".jsonl"
}

// repair is the single authority: a full-consent sessions/*/audit.jsonl stays
// tracked in a directory that matches its session and in one that does not;
// a blame-mode legacy one in a non-matching directory is untracked.
func TestUntrackLocalAuditJSONLFollowsRepair(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	const s = ".punt-labs/ethos/sessions/"
	fullMatch := s + "2026-11-09-sess-x-0001/audit.jsonl"
	fullOther := s + "sess-x-0002/audit.jsonl"
	blameOther := s + "sess-x-0003/audit.jsonl"
	writeTracked(t, dir, map[string]string{fullMatch: fullBody, fullOther: fullBody, blameOther: blameBody})

	out := runUntrack(t, untrackEnv(dir, &untrackLog{}))
	assert.Equal(t, []string{blameOther}, stagedDeletions(t, dir))
	assert.Equal(t, 1, out.Items)
}

// Step 5 (record-tree-trace) removes the working file after archiving it, so
// step 9 sees a path that is in the index and gone from disk.
func TestUntrackLocalReadsIndexBlobWhenWorkingFileIsGone(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	legacy := legacyChunk("2026-11-09-sess-gone-0001")
	full := ".punt-labs/ethos/sessions/2026-11-09-sess-gone-0002/audit-" + audit.TSToField(3) + "-" + audit.TSToField(4) + ".jsonl"
	writeTracked(t, dir, map[string]string{legacy: blameBody, full: fullBody})
	require.NoError(t, os.Remove(filepath.Join(dir, legacy)))
	require.NoError(t, os.Remove(filepath.Join(dir, full)))

	out := runUntrack(t, untrackEnv(dir, &untrackLog{}))
	assert.Empty(t, out.Residuals)
	assert.False(t, out.Deferred)
	assert.Equal(t, 1, out.Items)
	assert.Equal(t, []string{legacy}, stagedDeletions(t, dir))
}

// A path readable from neither the disk nor the index is a temporary
// condition: Deferred, never a Residual.
func TestUntrackLocalDefersWhenNeitherDiskNorIndexReadable(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	legacy := legacyChunk("2026-11-09-sess-gone-0003")
	writeTracked(t, dir, map[string]string{legacy: blameBody})
	require.NoError(t, os.Remove(filepath.Join(dir, legacy)))
	sha := gitIn(t, dir, "rev-parse", ":"+legacy)
	obj := filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])
	require.NoError(t, os.Remove(obj))

	out := runUntrack(t, untrackEnv(dir, &untrackLog{}))
	assert.True(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Zero(t, out.Items)
}

// A Residual reason is the repo-relative path and a fixed cause word; the
// checkout path and the home directory never appear in it.
func TestUntrackLocalResidualReasonCarriesNoRawPath(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	legacy := legacyChunk("2026-11-09-sess-perm-0001")
	writeTracked(t, dir, map[string]string{legacy: blameBody})
	require.NoError(t, os.Chmod(filepath.Join(dir, legacy), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, legacy), 0o644) })
	if _, err := os.ReadFile(filepath.Join(dir, legacy)); err == nil {
		t.Skip("file mode is not enforced for this user")
	}
	var logged []string
	env := untrackEnv(dir, &errLog{&logged})

	out := runUntrack(t, env)
	require.Len(t, out.Residuals, 1)
	r := out.Residuals[0]
	assert.Equal(t, legacy, r.ID)
	assert.NotContains(t, r.Reason, dir)
	assert.NotContains(t, r.Reason, os.TempDir())
	if home := os.Getenv("HOME"); home != "" {
		assert.NotContains(t, r.Reason, home)
	}
	assert.False(t, strings.Contains(r.Reason, "open "), "not the raw os error: %q", r.Reason)
}

type errLog struct{ lines *[]string }

func (l *errLog) Logf(step, action string, paths []string, err error) {
	s := step + " " + action
	if err != nil {
		s += " " + err.Error()
	}
	*l.lines = append(*l.lines, s)
}

// The pre-commit pass only unstages .punt-labs/local/; it never stages a
// record-tree deletion into the user's commit. The full pass does.
func TestPreCommitDoesNotUntrackRecordTreeTrace(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	legacy := legacyChunk("2026-11-09-sess-pc-0001")
	writeTracked(t, dir, map[string]string{legacy: blameBody})
	saved := Steps
	t.Cleanup(func() { Steps = saved })
	Steps = nil
	Register(Step{Name: "untrack-local", Scope: ScopeRepository, Run: untrackLocal})
	env := untrackEnv(dir, &untrackLog{})

	_, err := PreCommit(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, stagedDeletions(t, dir), "pre-commit must not stage record-tree deletions")

	out := runUntrack(t, env)
	assert.Equal(t, 1, out.Items)
	assert.Equal(t, []string{legacy}, stagedDeletions(t, dir))
}
