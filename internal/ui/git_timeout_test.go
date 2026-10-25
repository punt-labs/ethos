package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A blame whose git does not answer yields no lines and says why on stderr:
// the page shows no attribution, and the log shows the timeout.
func TestGitBlameGitTimeout(t *testing.T) {
	testhelpers.HangingGit(t)
	s := &Server{repoRoot: t.TempDir()}
	out := testhelpers.CaptureStderr(t, func() {
		assert.Nil(t, s.gitBlameWithTrailers("a.go"))
	})
	assert.Contains(t, out, "did not answer")
}

// A commit lookup that failed is not cached: the next line of the same commit
// asks git again, so one transient failure does not blank the whole file.
func TestLookupCommitDoesNotCacheAFailure(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	sha := strings.Repeat("a", 40)
	script := `#!/bin/sh
PATH=/usr/bin:/bin
case "$1" in
blame)
  printf '` + sha + ` 1 1 2\nauthor A\n\tone\n` + sha + ` 2 2\nauthor A\n\ttwo\n' ;;
log)
  case "$2" in --format=%s) echo 'no ticket here'; exit 0;; esac
  echo x >> ` + count + `
  if [ "$(wc -l < ` + count + `)" = 1 ]; then echo 'fatal: transient' >&2; exit 128; fi
  printf 'm-2026-01-01-001\nd-1\n' ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin)
	s := &Server{repoRoot: t.TempDir(), storeRoot: t.TempDir()}
	var lines []blameLine
	testhelpers.CaptureStderr(t, func() { lines = s.gitBlameWithTrailers("a.go") })
	require.Len(t, lines, 2)
	assert.Empty(t, lines[0].Mission, "the failed lookup has no mission")
	assert.Equal(t, "m-2026-01-01-001", lines[1].Mission, "the second line retried and got it")
}
