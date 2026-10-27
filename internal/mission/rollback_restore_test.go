//go:build !windows

package mission

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// nameTheHome rewrites the contract at path, with the raw marshal and no
// redaction, so that it holds this machine's home as an older ethos left it.
func nameTheHome(t *testing.T, s *Store, id, path, home string) []byte {
	t.Helper()
	c, err := s.Load(id)
	require.NoError(t, err)
	c.Context = "see " + filepath.Join(home, "old", "notes")
	b, err := yaml.Marshal(c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o600))
	return b
}

func assertRolledBack(t *testing.T, err error, notice, path string, want []byte) {
	t.Helper()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rolled back")
	assert.NotContains(t, err.Error(), "rollback failed")
	got, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, string(want), string(got), "a rollback restores the bytes that were on disk")
	assert.NotContains(t, notice, "was rewritten", "a rolled-back write announces nothing")
}

// TestRollback_RestoresBytesThatNameTheHome: a rollback puts back what was on
// disk, byte for byte, even when those bytes name $HOME; writing them through
// the sink guard would refuse them and leave the change in place with no event.
func TestRollback_RestoresBytesThatNameTheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("abandon", func(t *testing.T) {
		repoRoot := t.TempDir()
		s := NewStoreWithRoots(repoRoot, t.TempDir())
		id := "m-2026-08-06-130"
		require.NoError(t, s.Create(newContract(id)))
		path := mustContractPath(t, s, id)
		want := nameTheHome(t, s, id, path, home)
		logPath := audit.LiveMissionLogPath(repoRoot, id, sessionlessID)
		require.NoError(t, os.Remove(logPath))
		require.NoError(t, os.Mkdir(logPath, 0o700))
		var err error
		notice := captureStderr(t, func() { _, err = s.Abandon(id, "gone") })
		assertRolledBack(t, err, notice, path, want)
	})

	t.Run("force release", func(t *testing.T) {
		s := testStore(t)
		id := "m-2026-08-06-131"
		require.NoError(t, s.Create(newContract(id)))
		path := mustContractPath(t, s, id)
		want := nameTheHome(t, s, id, path, home)
		logPath := mustLogPath(t, s, id)
		require.NoError(t, os.Remove(logPath))
		require.NoError(t, os.Mkdir(logPath, 0o700))
		var err error
		notice := captureStderr(t, func() { _, err = s.ForceReleaseWriteSet(id, "release") })
		assertRolledBack(t, err, notice, path, want)
	})

	t.Run("disclaim", func(t *testing.T) {
		repoRoot := t.TempDir()
		s := NewStoreWithRoots(repoRoot, t.TempDir())
		c := newContract("m-2026-09-08-818")
		require.NoError(t, s.Create(c))
		did := "d-2026-09-08-016"
		recordPath, err := WriteDelegationSkeleton(repoRoot, c.MissionID, did, DelegationSkeleton{
			Tier: TierB, AgentType: c.Worker, BoundVia: BoundViaSidecarDispatch,
		})
		require.NoError(t, err)
		require.NoError(t, CloseDelegationSkeleton(repoRoot, c.MissionID, did, DelegationVerdictPass, "2026-09-08T12:00:00Z"))
		d, err := LoadDelegation(recordPath)
		require.NoError(t, err)
		d.Session = filepath.Join(home, "old", "session")
		want, err := yaml.Marshal(d)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(recordPath, want, 0o600))
		logPath := audit.LiveMissionLogPath(repoRoot, c.MissionID, sessionlessID)
		require.NoError(t, os.Remove(logPath))
		require.NoError(t, os.Mkdir(logPath, 0o700))
		notice := captureStderr(t, func() { _, err = s.DisclaimDelegation(c.MissionID, did, "unrelated") })
		assertRolledBack(t, err, notice, recordPath, want)
	})
}

// TestRollback_ResultAndReflectionRestoreTheirBytes: a result or a
// reflection whose event append fails puts back the file as it was loaded,
// byte for byte, though those bytes name $HOME, and prints no rewrite notice.
func TestRollback_ResultAndReflectionRestoreTheirBytes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, t.TempDir())
	c := newContract("m-2026-08-06-132")
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictFail)))
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)

	// Older bytes that name the home, written raw.
	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	rs[0].Prose = "see " + filepath.Join(home, "old")
	resBytes, err := yaml.Marshal(resultsFile{Results: rs})
	require.NoError(t, err)
	resPath := mustResultsPath(t, s, c.MissionID)
	require.NoError(t, os.WriteFile(resPath, resBytes, 0o600))
	fl, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	fl[0].Reason = "see " + filepath.Join(home, "old")
	flBytes, err := yaml.Marshal(reflectionsFile{Reflections: fl})
	require.NoError(t, err)
	flPath := mustReflectionsPath(t, s, c.MissionID)
	require.NoError(t, os.WriteFile(flPath, flBytes, 0o600))

	logPath := audit.LiveMissionLogPath(repoRoot, c.MissionID, sessionlessID)
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o700))

	notice := captureStderr(t, func() {
		err = s.AppendResult(c.MissionID, resultFor(loaded, VerdictPass))
	})
	assertRolledBack(t, err, notice, resPath, resBytes)
	notice = captureStderr(t, func() {
		err = s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 2, RecommendationContinue))
	})
	assertRolledBack(t, err, notice, flPath, flBytes)
}

// TestRollback_AnEmptyListFileIsRestoredNotDeleted: a results or reflections
// file that decodes to no entries (an empty or null list, with comments) is
// still a file that was on disk; a failed append puts it back byte for byte
// rather than removing it. Only the null-list subcases guard the fix (they
// decode to a nil list); the empty-list subcases pin behaviour that already held.
func TestRollback_AnEmptyListFileIsRestoredNotDeleted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for name, c := range map[string]struct {
		body string
		path func(*testing.T, *Store, string) string
		do   func(*Store, *Contract) error
	}{
		"results, null list": {"# kept\nresults:\n", mustResultsPath, func(s *Store, c *Contract) error {
			return s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
		}},
		"results": {"# kept\nresults: []\n", mustResultsPath, func(s *Store, c *Contract) error {
			return s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
		}},
		"reflections, null list": {"# kept\nreflections:\n", mustReflectionsPath, func(s *Store, c *Contract) error {
			return s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
		}},
		"reflections": {"# kept\nreflections: []\n", mustReflectionsPath, func(s *Store, c *Contract) error {
			return s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			repoRoot := t.TempDir()
			s := NewStoreWithRoots(repoRoot, t.TempDir())
			ct := newContract("m-2026-08-06-133")
			require.NoError(t, s.Create(ct))
			path := c.path(t, s, ct.MissionID)
			require.NoError(t, os.WriteFile(path, []byte(c.body), 0o600))
			logPath := audit.LiveMissionLogPath(repoRoot, ct.MissionID, sessionlessID)
			require.NoError(t, os.Remove(logPath))
			require.NoError(t, os.Mkdir(logPath, 0o700))
			var err error
			notice := captureStderr(t, func() { err = c.do(s, ct) })
			assertRolledBack(t, err, notice, path, []byte(c.body))
		})
	}
}
