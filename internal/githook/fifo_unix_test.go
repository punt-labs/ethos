//go:build unix

package githook

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// hooksDirWithin runs HooksDir(dir) and fails the test if it does not return
// within the timeout: a FIFO opened for reading blocks forever.
func hooksDirWithin(t *testing.T, dir string) Lookup {
	t.Helper()
	done := make(chan Lookup, 1)
	go func() { done <- HooksDir(dir) }()
	select {
	case l := <-done:
		return l
	case <-time.After(5 * time.Second):
		t.Fatalf("HooksDir(%s) blocked: it opened a FIFO", dir)
		return Lookup{}
	}
}

// A .git that is a FIFO is never opened, with git or without: the manual
// fallback reads only regular files, by resolve's .git rules.
func TestHooksDirNeverOpensAFIFO(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		t.Run(map[bool]string{true: "git", false: "no git"}[withGit], func(t *testing.T) {
			dir := t.TempDir()
			if err := syscall.Mkfifo(filepath.Join(dir, ".git"), 0o644); err != nil {
				t.Fatal(err)
			}
			if !withGit {
				t.Setenv("PATH", t.TempDir())
			}
			l := hooksDirWithin(t, dir)
			if !l.Degraded {
				t.Errorf("lookup for a FIFO .git is not degraded: %+v", l)
			}
		})
	}
}

// Without git, a commondir that is a FIFO is not opened either. (git itself
// blocks reading one, so there is no answer to compare against with git.)
func TestManualHooksDirFIFOCommondir(t *testing.T) {
	dir := t.TempDir()
	admin := filepath.Join(t.TempDir(), "admin")
	makeGitDir(t, admin)
	if err := syscall.Mkfifo(filepath.Join(admin, "commondir"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".git"), "gitdir: "+admin+"\n")
	t.Setenv("PATH", t.TempDir())
	l := hooksDirWithin(t, dir)
	if !l.Degraded {
		t.Errorf("lookup without git is not degraded: %+v", l)
	}
}
