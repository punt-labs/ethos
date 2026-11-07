package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func readOptionalEventBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	return b
}

func TestRepositoryOrdinaryEventDirectoryFailureRollsBack(t *testing.T) {
	for _, operation := range []string{"result", "advance", "close", "abandon", "update"} {
		for _, session := range []string{"original", "new-stream"} {
			// Fail each required directory confirmation, not just the live
			// file's immediate parent. Authority installation has separate syncs.
			for depth := range 6 {
				t.Run(fmt.Sprintf("%s/%s/ancestor-%d", operation, session, depth), func(t *testing.T) {
					s, c := authorityStore(t, 2)
					switch operation {
					case "close":
						require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))
					case "advance":
						require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)))
					}
					s = s.WithSessionID(session)
					live := (audit.Stream{Namespace: audit.MissionNS, Mission: c.MissionID, Session: session}).LivePath(s.policy)
					before := readOptionalEventBytes(t, live)
					authority := map[string][]byte{}
					for _, name := range []string{"contract.yaml", "results.yaml", "corrections.yaml"} {
						path := filepath.Join(s.policy.RecordRoot(), "missions", c.MissionID, name)
						authority[path] = readOptionalEventBytes(t, path)
					}
					apply := func() error {
						switch operation {
						case "result":
							return s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
						case "advance":
							_, err := s.AdvanceRound(c.MissionID, c.Leader)
							return err
						case "close":
							_, err := s.Close(c.MissionID, StatusClosed)
							return err
						case "abandon":
							_, err := s.Abandon(c.MissionID, "cancelled")
							return err
						default:
							loaded, err := s.Load(c.MissionID)
							if err != nil {
								return err
							}
							loaded.Inputs.References = []string{"updated-reference"}
							return s.Update(loaded)
						}
					}
					failDir := filepath.Dir(live)
					for range depth {
						failDir = filepath.Dir(failDir)
					}
					originalSync := syncDir
					t.Cleanup(func() { syncDir = originalSync })
					injected := errors.New("injected required event directory sync failure")
					inStream, failed := false, false
					syncDir = func(dir string) error {
						inStream = inStream || dir == filepath.Dir(live)
						if inStream && dir == failDir && !failed {
							failed = true
							return injected
						}
						return originalSync(dir)
					}
					err := apply()
					require.True(t, failed, "must exercise the required stream directory sync")
					require.ErrorIs(t, err, injected)
					assert.Contains(t, err.Error(), "rolled back")
					assert.NotContains(t, err.Error(), "rollback failed")
					assert.True(t, bytes.Equal(before, readOptionalEventBytes(t, live)), "failed operation must preserve exact event bytes")
					for path, want := range authority {
						if want == nil {
							assert.NoFileExists(t, path, "rollback must preserve absent authority")
						}
						assert.True(t, bytes.Equal(want, readOptionalEventBytes(t, path)), "failed operation must preserve exact authority bytes at %s", path)
					}
					syncDir = originalSync
					require.NoError(t, apply())
					after := readOptionalEventBytes(t, live)
					require.True(t, bytes.HasPrefix(after, before))
					added := after[len(before):]
					require.Equal(t, 1, bytes.Count(added, []byte{'\n'}), "retry must append exactly one event")
					var event Event
					require.NoError(t, json.Unmarshal(added, &event))
					want := operation
					if want == "advance" {
						want = "round_advanced"
					}
					assert.Equal(t, want, event.Event)
				})
			}
		}
	}
}

func TestRepositoryEventConfirmsNewNameBeforeAppend(t *testing.T) {
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
	require.NoError(t, err)
	s := NewRepositoryStore(p, t.TempDir()).WithSessionID("first-stream")
	id := "m-2026-11-05-125"
	live := (audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "first-stream"}).LivePath(p)
	assert.NoDirExists(t, filepath.Dir(live))
	originalSync := syncDir
	t.Cleanup(func() { syncDir = originalSync })
	var confirmed []string
	syncDir = func(dir string) error {
		info, err := os.Stat(live)
		require.NoError(t, err, "the new live name must exist before confirming its directory")
		require.True(t, info.Mode().IsRegular())
		assert.Zero(t, info.Size(), "no event may be written before all directory confirmations")
		confirmed = append(confirmed, dir)
		return originalSync(dir)
	}
	require.NoError(t, s.appendRepositoryEvent(id, Event{Event: "create", Actor: "claude"}))
	var want []string
	root := filepath.Dir(filepath.Dir(p.RecordRoot()))
	for dir := filepath.Dir(live); ; dir = filepath.Dir(dir) {
		want = append(want, dir)
		if dir == root {
			break
		}
	}
	assert.Equal(t, want, confirmed)
	assert.Equal(t, 1, bytes.Count(readOptionalEventBytes(t, live), []byte{'\n'}))
}

func TestRepositoryEventConcurrentNewStream(t *testing.T) {
	p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
	require.NoError(t, err)
	const writers = 12
	id := "m-2026-11-05-126"
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			<-start
			s := NewRepositoryStore(p, "").WithSessionID("same-stream")
			errs <- s.appendRepositoryEvent(id, Event{Event: "update", Actor: "claude"})
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	data, err := audit.ReadStream(p, audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "same-stream"})
	require.NoError(t, err)
	require.Len(t, data.Lines, writers)
	for i := 1; i < len(data.Lines); i++ {
		assert.Greater(t, data.Lines[i].TS, data.Lines[i-1].TS)
	}
}
