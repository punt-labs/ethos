package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriters_FailingWritesPrintNoRewriteNotice drives each record writer
// through a write that fails, with content the record pass rewrites, and
// asserts that no notice says a rewrite happened: nothing was stored.
func TestWriters_FailingWritesPrintNoRewriteNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	leak := filepath.Join(home, "secret", "place")
	const id = "m-2026-08-06-120"

	// blockWith makes the file at path a directory, so a rename over it fails.
	blockWith := func(t *testing.T, path string) {
		t.Helper()
		_ = os.Remove(path)
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), nil, 0o600))
	}
	newStore := func(t *testing.T) *Store {
		s := NewStoreWithRoots(t.TempDir(), t.TempDir())
		require.NoError(t, s.Create(newContract(id)))
		return s
	}

	cases := map[string]func(t *testing.T){
		"contract": func(t *testing.T) {
			s := newStore(t)
			p, err := s.contractPath(id)
			require.NoError(t, err)
			blockWith(t, p)
			c := newContract(id)
			c.Context = "see " + leak
			require.Error(t, s.writeContract(c))
		},
		"results": func(t *testing.T) {
			s := newStore(t)
			p, err := s.resultsPath(id)
			require.NoError(t, err)
			blockWith(t, p)
			require.Error(t, s.writeResultsLocked(id, []Result{{Mission: id, Round: 1, Prose: "see " + leak}}))
		},
		"reflections": func(t *testing.T) {
			s := newStore(t)
			p, err := s.reflectionsPath(id)
			require.NoError(t, err)
			blockWith(t, p)
			require.Error(t, s.writeReflectionsLocked(id, []Reflection{{Mission: id, Round: 1, Reason: "see " + leak}}))
		},
		"event": func(t *testing.T) {
			s := newStore(t)
			// The live log directory is a regular file: the append fails.
			zone := filepath.Join(s.repoRoot, ".punt-labs", "local")
			require.NoError(t, os.RemoveAll(zone))
			require.NoError(t, os.MkdirAll(filepath.Dir(zone), 0o700))
			require.NoError(t, os.WriteFile(zone, nil, 0o600))
			require.Error(t, s.appendEventLocked(id, Event{TS: "t", Event: "e", Actor: "a",
				Details: map[string]any{"reason": "see " + leak}}))
		},
		"delegation record": func(t *testing.T) {
			repo := t.TempDir()
			blockWith(t, filepath.Join(DelegationDir(repo, id, "d-1"), "record.yaml"))
			_, err := WriteDelegationSkeleton(repo, id, "d-1", DelegationSkeleton{
				Tier: TierB, AgentType: "bwk", Session: leak,
			})
			require.Error(t, err)
		},
		"migrate copy": func(t *testing.T) {
			r, err := NewPathRedactor(t.TempDir())
			require.NoError(t, err)
			src := filepath.Join(t.TempDir(), "results.yaml")
			require.NoError(t, os.WriteFile(src, []byte("results:\n  - mission: "+id+"\n    round: 1\n    prose: see "+leak+"\n"), 0o600))
			blocker := filepath.Join(t.TempDir(), "file")
			require.NoError(t, os.WriteFile(blocker, nil, 0o600))
			require.Error(t, copyIfExists(r, src, filepath.Join(blocker, "results.yaml")))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			notice := captureStderr(t, func() { run(t) })
			for _, p := range []string{"was rewritten before it was stored", "migrated copy"} {
				assert.NotContains(t, notice, p, "a failed write must not announce a rewrite: %s", notice)
			}
		})
	}
}
