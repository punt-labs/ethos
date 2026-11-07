package mission

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Unlike repository reflection tests (which dispatch to ApplyReflection), this
// exercises AppendReflection's legacy restoreAtomicFile(..., prev) rollback with
// a nonempty prior file. Reconstructing equivalent YAML would lose its comments
// and formatting; rollback must restore the saved bytes, not regenerate them.
func TestLegacyAppendReflectionRestoresExistingBytes(t *testing.T) {
	s := testStore(t)
	require.False(t, s.twoTreeStorage, "AppendReflection must not dispatch to repository ApplyReflection")
	require.Empty(t, s.repoRoot, "use the standalone legacy store, not a repository fixture")
	c := newContract("m-2026-11-05-152")
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictFail)))
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
	round, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	require.Equal(t, 2, round)

	// Valid older YAML, deliberately not the serializer's representation. No
	// credentials are needed: byte identity includes comments, key order,
	// quoting, indentation and blank lines as well as the reflection values.
	saved := []byte(`# retained legacy reflection, including hand-written formatting
reflections:
  - round: 1 # keep this inline comment too
    mission: "m-2026-11-05-152"
    author: 'claude'
    created_at: "2026-11-05T00:00:00Z"
    recommendation: continue
    converging: true
    signals: ["all green"]
    reason: 'round one complete'

`)
	path := mustReflectionsPath(t, s, c.MissionID)
	require.NoError(t, os.WriteFile(path, saved, 0o600))
	prior, prev, err := s.loadReflectionsBytes(c.MissionID)
	require.NoError(t, err, "the pre-existing file must contain valid reflections")
	require.NotNil(t, prev, "exercise restoration, not the absent-file removal branch")
	require.Equal(t, saved, prev)
	require.Len(t, prior, 1)
	require.Equal(t, 1, prior[0].Round)
	regenerated, err := yaml.Marshal(reflectionsFile{Reflections: prior})
	require.NoError(t, err)
	require.NotEqual(t, saved, regenerated, "equivalent YAML is not a byte-preserving rollback")
	contractPath := mustContractPath(t, s, c.MissionID)
	contractBefore, err := os.ReadFile(contractPath)
	require.NoError(t, err)

	// A directory at the legacy log path deterministically rejects the required
	// event's OpenFile after the reflections rewrite, even when run as root.
	logPath, err := s.logPath(c.MissionID)
	require.NoError(t, err)
	logBefore, err := os.ReadFile(logPath)
	require.NoError(t, err)
	eventsBefore, warnings, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o700))
	next := reflectionFor(c.MissionID, round, RecommendationContinue)
	require.False(t, s.twoTreeStorage, "the failing append must still take the legacy branch")
	err = s.AppendReflection(c.MissionID, next)
	require.ErrorContains(t, err, "event append failed, reflection rolled back")
	require.ErrorContains(t, err, "opening event log")
	require.NotContains(t, err.Error(), "rollback failed")
	assert.Empty(t, next.CreatedAt, "a failed append must not report successful persistence to its caller")
	restored, err := os.ReadFile(path)
	require.NoError(t, err)
	if !assert.Equal(t, saved, restored, "legacy rollback must restore the exact pre-existing reflection bytes") {
		return
	}
	loaded, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, prior, loaded, "the failed round must not become observable")
	contractAfter, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	assert.Equal(t, contractBefore, contractAfter)

	// Unblock only this fixture's log and retry the same request. A restored
	// prior file must not leave a duplicate-round refusal or a ghost event.
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.WriteFile(logPath, logBefore, 0o600))
	require.NoError(t, s.AppendReflection(c.MissionID, next))
	assert.NotEmpty(t, next.CreatedAt)
	loaded, err = s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	assert.Equal(t, prior[0], loaded[0])
	assert.Equal(t, *next, loaded[1])
	eventsAfter, warnings, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, eventsAfter, len(eventsBefore)+1)
	assert.Equal(t, eventsBefore, eventsAfter[:len(eventsBefore)])
	last := eventsAfter[len(eventsBefore)]
	assert.Equal(t, "reflect", last.Event)
	assert.Equal(t, next.CreatedAt, last.TS)
	assert.Equal(t, next.Author, last.Actor)
	assert.Equal(t, float64(round), last.Details["round"])
	assert.Empty(t, last.OperationID, "legacy append must not create a repository prepared operation")
}
