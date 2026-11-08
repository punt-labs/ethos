package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func archiveIdentityStore(t *testing.T) (*Store, *Contract, []reflectionIntent) {
	t.Helper()
	s, c := authorityStore(t, 3)
	var archives []reflectionIntent
	for round := 1; round <= 2; round++ {
		if round == 2 {
			n, err := s.AdvanceRound(c.MissionID, c.Leader)
			require.NoError(t, err)
			require.Equal(t, 2, n)
		}
		out, err := s.ApplyReflection(c.MissionID, reflectionFor(c.MissionID, round, RecommendationContinue))
		require.NoError(t, err)
		require.Equal(t, "applied", out.Application)
		require.Equal(t, "confirmed", out.Durability)
		require.Equal(t, "complete", out.Trace)
		in, err := readIntent(s.reflectionArchivePath(c.MissionID, round), c.MissionID)
		require.NoError(t, err)
		require.NotNil(t, in)
		archives = append(archives, *in)
	}
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	require.Len(t, loaded.RoundDecisions.Entries, 2)
	events, warnings, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	for _, in := range archives {
		count := 0
		for _, event := range events {
			if event.OperationID == in.Decision.ID {
				count++
				require.Equal(t, "original", in.EventSession)
				require.True(t, sameJSON(in.Event, Event{TS: event.TS, Event: event.Event, Actor: event.Actor,
					Details: event.Details, Record: event.Record, OperationID: event.OperationID}))
				require.Equal(t, "accepted", event.Acceptance)
			}
		}
		require.Equal(t, 1, count, "each decision has its original required event")
	}
	return s, loaded, archives
}

func TestReflectionArchiveIdentity(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(fmt.Sprintf("published-mirror=%t", mirror), func(t *testing.T) {
			s, c, archives := archiveIdentityStore(t)
			if mirror {
				data, err := yaml.Marshal(reflectionsFile{Reflections: []Reflection{archives[0].Reflection}})
				require.NoError(t, err)
				path := filepath.Join(s.policy.CheckoutRecordRoot(), "missions", c.MissionID, "reflections.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			// Retain the exact accepted receipts as if optional cleanup had not
			// finished. Remove only round 1's receipt; round 2 stays readable.
			for _, in := range archives {
				data, err := yaml.Marshal(in)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(s.intentPath(c.MissionID, in.Decision.Round), data, 0o600))
			}
			require.NoError(t, os.Remove(s.intentPath(c.MissionID, 1)))
			states, err := s.ReadReflections(c.MissionID)
			require.NoError(t, err)
			require.Len(t, states, 2)
			for _, state := range states {
				require.Equal(t, "present", state.DetailState, "healthy control")
				require.Empty(t, state.Diagnostics)
			}
			archivePath := s.reflectionArchivePath(c.MissionID, 1)
			data, err := os.ReadFile(s.reflectionArchivePath(c.MissionID, 2))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(archivePath, data, 0o600))
			// The bytes are a valid envelope for this mission, but not this path.
			misplaced, err := readIntent(archivePath, c.MissionID)
			require.NoError(t, err)
			require.Equal(t, archives[1], *misplaced)
			before := legacyReflectionTree(t, s.repoRoot)
			checkoutBefore := legacyReflectionTree(t, s.checkoutRoot)
			globalBefore := legacyReflectionTree(t, s.root)
			check := func(states []ReflectionRead, err error) {
				t.Helper()
				require.NoError(t, err)
				require.Len(t, states, 2)
				assert.Equal(t, "present", states[0].DecisionState)
				assert.Equal(t, &archives[0].Decision, states[0].Decision)
				assert.Equal(t, "unreadable", states[0].DetailState)
				assert.Nil(t, states[0].Reflection)
				assert.False(t, states[0].Pending)
				diagnostic := strings.Join(states[0].Diagnostics, "\n")
				for _, want := range []string{"reflection archive", archivePath, "requested round 1", "actual round 2"} {
					assert.Contains(t, diagnostic, want)
				}
				assert.Equal(t, "present", states[1].DecisionState)
				assert.Equal(t, &archives[1].Decision, states[1].Decision)
				assert.Equal(t, "present", states[1].DetailState)
				assert.Equal(t, &archives[1].Reflection, states[1].Reflection)
				assert.True(t, states[1].Pending)
				assert.Empty(t, states[1].Diagnostics)
			}
			for range 2 {
				check(s.ReadReflections(c.MissionID))
				reflections, err := s.LoadReflections(c.MissionID)
				assert.ErrorContains(t, err, "reflection detail unreadable")
				assert.Nil(t, reflections)
			}
			// Concurrent readers must neither recover receipts nor hide damage.
			type readResult struct {
				states []ReflectionRead
				err    error
			}
			results := make(chan readResult, 8)
			var wg sync.WaitGroup
			for range cap(results) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					states, err := s.ReadReflections(c.MissionID)
					results <- readResult{states, err}
				}()
			}
			wg.Wait()
			close(results)
			for result := range results {
				check(result.states, result.err)
			}
			assert.Equal(t, before, legacyReflectionTree(t, s.repoRoot))
			assert.Equal(t, checkoutBefore, legacyReflectionTree(t, s.checkoutRoot))
			assert.Equal(t, globalBefore, legacyReflectionTree(t, s.root))
			loaded, err := s.Load(c.MissionID)
			require.NoError(t, err)
			assert.Equal(t, c, loaded)
		})
	}
}
