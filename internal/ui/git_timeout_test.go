package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A commit lookup that failed is remembered for the rest of that request, so a
// page does not ask git again for every line of the same commit; the next
// request asks afresh.
func TestLookupCommitCachesAFailureForTheRequestOnly(t *testing.T) {
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
	var first []blameLine
	testhelpers.CaptureStderr(t, func() { first = s.gitBlameWithTrailers("a.go") })
	require.Len(t, first, 2)
	assert.Empty(t, first[0].Mission, "the failed lookup has no mission")
	assert.Empty(t, first[1].Mission, "the second line reuses the failed answer for this request")
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "x"), "git log was asked once for the commit")

	var next []blameLine
	testhelpers.CaptureStderr(t, func() { next = s.gitBlameWithTrailers("a.go") })
	require.Len(t, next, 2)
	assert.Equal(t, "m-2026-01-01-001", next[0].Mission, "the next request asks again")
}

// A git that hangs costs one timeout per page, not one per commit: after the
// first timeout the rest of the request does not ask git again.
func TestBlamePageStopsAskingGitAfterATimeout(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	sha1, sha2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	script := `#!/bin/sh
PATH=/usr/bin:/bin
case "$1" in
blame)
  printf '` + sha1 + ` 1 1 1\nauthor A\n\tone\n` + sha2 + ` 2 2 1\nauthor B\n\ttwo\n' ;;
log)
  echo x >> ` + count + `
  exec ` + sleep + ` 60 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin)
	testhelpers.GitTimeout(t, 200*time.Millisecond)
	s := &Server{repoRoot: t.TempDir(), storeRoot: t.TempDir()}
	testhelpers.CaptureStderr(t, func() { s.gitBlameWithTrailers("a.go") })
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "x"), "one timeout for the page, not one per commit")
}
