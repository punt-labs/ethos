//go:build !windows

package autorepair

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tracked symlink at a legacy-named record-tree path must never be followed:
// a link to a FIFO would block the step forever, inside the global repair lock.
// The entry is a fixed-reason Residual and nothing is staged.
func TestUntrackLocalSymlinkToFIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	rel := legacyChunk("2026-11-09-sess-fifo-0001")
	p := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.Symlink(fifo, p))
	gitIn(t, dir, "add", "-f", "--", rel)
	gitIn(t, dir, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "x")

	done := make(chan Outcome, 1)
	go func() { done <- runUntrack(t, untrackEnv(dir, &recLog{})) }()
	select {
	case o := <-done:
		assert.False(t, o.Deferred)
		require.Len(t, o.Residuals, 1)
		assert.Equal(t, "tracked entry is not a regular file: "+rel, o.Residuals[0].Reason)
		assert.Empty(t, stagedDeletions(t, dir))
	case <-time.After(2 * time.Second):
		t.Error("untrack-local blocked reading a tracked symlink to a FIFO")
		f, _ := os.OpenFile(fifo, os.O_WRONLY, 0) // unblock the reader so the goroutine ends
		_ = f.Close()
		<-done
	}
}

// A gitlink (mode 160000) has no blob: `git cat-file blob` exits 128 on it every
// run, which is permanent, not transient. It is one Residual, never Deferred.
func TestUntrackLocalGitlinkIsResidualNotDeferred(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	rel := legacyChunk("2026-11-09-sess-g-0001")
	gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,"+rel)
	out, err := exec.Command("git", "-C", dir, "cat-file", "blob", ":0:"+rel).CombinedOutput()
	require.Error(t, err, "premise: cat-file blob fails on a gitlink: %s", out)

	for run := 0; run < 2; run++ {
		o := runUntrack(t, untrackEnv(dir, &recLog{}))
		assert.False(t, o.Deferred, "run %d", run)
		require.Len(t, o.Residuals, 1, "run %d", run)
		assert.Equal(t, "tracked entry is not a file: "+rel, o.Residuals[0].Reason)
		assert.Empty(t, stagedDeletions(t, dir))
	}
}
