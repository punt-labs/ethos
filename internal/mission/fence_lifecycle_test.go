package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fencedMission returns a repository store holding a legacy open mission whose
// crossed-round history is incomplete: reflections exist for rounds 1 and 3 of
// a mission now in round 3, and round 2 is missing.
func fencedMission(t *testing.T) (*Store, *Contract, string) {
	t.Helper()
	s, c, root := legacyReflectionReader(t, true, 3)
	var rs []Reflection
	for _, r := range []int{1, 3} {
		x := *reflectionFor(c.MissionID, r, RecommendationContinue)
		x.CreatedAt = "2026-11-05T10:00:00Z"
		rs = append(rs, x)
	}
	b, err := yaml.Marshal(reflectionsFile{Reflections: rs})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustReflectionsPath(t, s, c.MissionID), b, 0o600))
	return s, c, root
}

// A fenced mission refuses every lifecycle write, and the refusal leaves the
// mission directory exactly as it was.
func TestFence_RefusesEveryLifecycleWrite(t *testing.T) {
	writes := map[string]func(s *Store, c *Contract) error{
		"AppendResult": func(s *Store, c *Contract) error {
			return s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
		},
		"Close": func(s *Store, c *Contract) error {
			_, err := s.Close(c.MissionID, StatusClosed)
			return err
		},
		"Update": func(s *Store, c *Contract) error {
			l, err := s.Load(c.MissionID)
			if err != nil {
				return err
			}
			l.Context = "edited"
			return s.Update(l)
		},
		"Abandon": func(s *Store, c *Contract) error {
			_, err := s.Abandon(c.MissionID, "probe")
			return err
		},
		"ForceReleaseWriteSet": func(s *Store, c *Contract) error {
			_, err := s.ForceReleaseWriteSet(c.MissionID, "probe")
			return err
		},
		"DisclaimDelegation": func(s *Store, c *Contract) error {
			_, err := s.DisclaimDelegation(c.MissionID, "d-2026-11-05-001", "probe")
			return err
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			s, c, root := fencedMission(t)
			before := recordFiles(t, root)
			err := write(s, c)
			require.Error(t, err)
			assert.ErrorContains(t, err, "fenced from lifecycle writes")
			assert.Equal(t, before, recordFiles(t, root), "a refused write changes nothing")
			assert.NoFileExists(t, filepath.Join(filepath.Dir(mustContractPath(t, s, c.MissionID)), "results.yaml"))
		})
	}
}

// Reads of a fenced mission keep working and show that authority is unavailable.
func TestFence_ReadsStillShowAuthorityUnavailable(t *testing.T) {
	s, c, _ := fencedMission(t)
	reads, err := s.ReadReflections(c.MissionID)
	require.NoError(t, err)
	require.NotEmpty(t, reads)
	for _, r := range reads {
		assert.Equal(t, "legacy_unknown", r.DecisionState)
	}
	_, err = s.Load(c.MissionID)
	require.NoError(t, err)
}

// A legacy mission whose history is complete still closes: the guard fences
// only what cannot be imported.
func TestFence_CompleteLegacyHistoryIsNotFenced(t *testing.T) {
	s, c, _ := legacyReflectionReader(t, true, 1)
	x := *reflectionFor(c.MissionID, 1, RecommendationContinue)
	x.CreatedAt = "2026-11-05T10:00:00Z"
	b, err := yaml.Marshal(reflectionsFile{Reflections: []Reflection{x}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustReflectionsPath(t, s, c.MissionID), b, 0o600))
	assert.NoError(t, s.requireImportableAuthority(c))
}

// recordFiles returns the shared record tree, without the lock files a refused
// write may legitimately create under the local zone.
func recordFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p, v := range legacyReflectionTree(t, root) {
		if !strings.HasPrefix(p, ".punt-labs/local") {
			out[p] = v
		}
	}
	return out
}
