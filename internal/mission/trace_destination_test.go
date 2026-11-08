//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraceTerminalSummaryRefusesSymlink(t *testing.T) {
	for _, operation := range []string{"close", "abandon"} {
		t.Run(operation, func(t *testing.T) {
			s, c := prepareOrdinaryMutation(t, operation)
			target := filepath.Join(t.TempDir(), "sentinel")
			before := []byte("external sentinel\n")
			require.NoError(t, os.WriteFile(target, before, 0o640))
			dest := RepoStatePath(s.repoRoot, "missions.jsonl")
			require.NoError(t, os.Symlink(target, dest))

			out, err := ordinaryMutation(s, c, operation)
			require.NoError(t, err, "optional summary failure must not undo accepted lifecycle authority")
			assertOrdinaryMutation(t, s, c, operation)
			assert.Len(t, out.Warnings, 1)
			if len(out.Warnings) > 0 {
				assert.Contains(t, out.Warnings[0], "trace write failed")
				assert.Contains(t, out.Warnings[0], "symlink")
			}
			events, warnings, err := s.LoadEvents(c.MissionID)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			require.NotEmpty(t, events)
			assert.Equal(t, operation, events[len(events)-1].Event)
			after, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			info, err := os.Stat(target)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
			link, err := os.Readlink(dest)
			require.NoError(t, err)
			assert.Equal(t, target, link)
		})
	}
}

func TestTraceAppendRefusesRedirectedAncestry(t *testing.T) {
	for _, component := range []string{"parent", "root", ".punt-labs", "ethos"} {
		t.Run(component, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "parent")
			root := filepath.Join(parent, "repo")
			dir := RepoStatePath(root)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			target := filepath.Join(t.TempDir(), "redirected")
			ancestor := map[string]string{"parent": parent, "root": root, ".punt-labs": filepath.Join(root, ".punt-labs"), "ethos": dir}[component]
			require.NoError(t, os.Rename(ancestor, target))
			require.NoError(t, os.Symlink(target, ancestor))
			sentinel := filepath.Join(target, "missions.jsonl")
			if component != "ethos" {
				rel, err := filepath.Rel(ancestor, filepath.Join(dir, "missions.jsonl"))
				require.NoError(t, err)
				sentinel = filepath.Join(target, rel)
			}
			before := []byte("external sentinel\n")
			require.NoError(t, os.WriteFile(sentinel, before, 0o600))
			err := (&Store{repoRoot: root}).appendTraceLine([]byte("{\"id\":\"m-safe\"}\n"))
			assert.ErrorContains(t, err, "symlink")
			after, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestTraceAppendChecksDestinationUnderSummaryLock(t *testing.T) {
	s, _ := authorityStore(t, 1)
	dest := RepoStatePath(s.repoRoot, "missions.jsonl")
	target := filepath.Join(t.TempDir(), "sentinel")
	before := []byte("external sentinel\n")
	require.NoError(t, os.WriteFile(target, before, 0o600))
	done := make(chan error, 1)
	require.NoError(t, WithSummaryLock(s.repoRoot, func() error {
		go func() { done <- s.appendTraceLine([]byte("{\"id\":\"m-safe\"}\n")) }()
		select {
		case err := <-done:
			t.Fatalf("append crossed summary lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		return os.Symlink(target, dest)
	}))
	select {
	case err := <-done:
		assert.ErrorContains(t, err, "symlink")
	case <-time.After(5 * time.Second):
		t.Fatal("append remained blocked after summary lock release")
	}
	after, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestTraceAppendRefusesNonRegularLeaf(t *testing.T) {
	for _, kind := range []string{"directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			dest := RepoStatePath(root, "missions.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
			if kind == "directory" {
				require.NoError(t, os.Mkdir(dest, 0o700))
			} else {
				require.NoError(t, syscall.Mkfifo(dest, 0o600))
				// Keep both ends open so a regressed append fails this assertion
				// instead of hanging while opening the FIFO for write only.
				f, err := os.OpenFile(dest, os.O_RDWR, 0)
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, f.Close()) })
			}
			before, err := os.Lstat(dest)
			require.NoError(t, err)
			err = (&Store{repoRoot: root}).appendTraceLine([]byte("{\"id\":\"m-safe\"}\n"))
			assert.ErrorContains(t, err, "not a regular file")
			after, err := os.Lstat(dest)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after))
			assert.Equal(t, before.Mode(), after.Mode())
		})
	}
}
