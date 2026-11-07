//go:build !windows

package ui

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// These requests use a real created, closed and corrected mission. Shared
// corrections must survive optional event damage; event copies must not repair
// or replace missing/invalid shared authority on a read (DES-079 / DES-072).
func TestHandleMissionIndependentHistory(t *testing.T) {
	for _, tc := range []struct {
		name            string
		wantCorrection  bool
		wantEvents      bool
		eventsError     string
		eventsWarning   string
		correctionError string
		acceptance      string
	}{
		{name: "healthy", wantCorrection: true, wantEvents: true, acceptance: "accepted"},
		{name: "absent-events", wantCorrection: true},
		{name: "malformed-live", wantCorrection: true, eventsError: "ui-history.log.jsonl line 1"},
		{name: "torn-live-tail", wantCorrection: true, wantEvents: true, eventsWarning: "ui-history.log.jsonl line", acceptance: "accepted"},
		{name: "malformed-legacy", wantCorrection: true, eventsError: "log.jsonl line 1"},
		{name: "invalid-authority", wantEvents: true, correctionError: "invalid corrections authority", eventsWarning: "correction acceptance unknown", acceptance: "unknown"},
		{name: "truncated-authority", wantEvents: true, correctionError: "yaml:", eventsWarning: "correction acceptance unknown", acceptance: "unknown"},
		{name: "missing-authority", wantEvents: true, correctionError: "corrections authority unavailable", eventsWarning: "correction acceptance unknown", acceptance: "unknown"},
		{name: "initialized-empty", wantEvents: true, acceptance: "uncommitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root, checkout, global := filepath.Join(base, "store"), filepath.Join(base, "checkout"), filepath.Join(base, "global")
			for _, dir := range []string{root, checkout, global} {
				require.NoError(t, os.MkdirAll(dir, 0o700))
			}
			id := "m-2026-11-05-200"
			store := mission.NewStoreWithRoots(root, global).WithCheckoutRoot(checkout).WithSessionID("ui-history")
			require.NoError(t, store.Create(uiTestContract(id)))
			authorityPath := mission.RepoStatePath(root, "missions", id, "corrections.yaml")
			emptyAuthority, err := os.ReadFile(authorityPath)
			require.NoError(t, err)
			require.NoError(t, store.AppendResult(id, &mission.Result{
				Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.9,
				Evidence: []mission.EvidenceCheck{{Name: "bounded check", Status: mission.EvidenceStatusPass}},
			}))
			_, err = store.Close(id, mission.StatusClosed)
			require.NoError(t, err)
			correction := mission.Correction{
				Mission: id, Round: 1, Kind: mission.CorrectionFactual, Author: "history-reviewer",
				Claim:     "the optional timeline is the correction authority",
				Corrected: "shared corrections remain authoritative without optional events",
			}
			require.NoError(t, store.Correct(id, correction))
			corrections, warnings, err := store.LoadCorrections(id)
			require.NoError(t, err)
			require.Empty(t, warnings)
			require.Equal(t, []mission.Correction{correction}, corrections)

			livePath := audit.LiveMissionLogPath(root, id, "ui-history")
			switch tc.name {
			case "absent-events":
				require.NoError(t, os.RemoveAll(audit.LocalZoneBase(root)))
			case "malformed-live":
				require.NoError(t, os.WriteFile(livePath, []byte("{malformed event\n"), 0o600))
			case "torn-live-tail":
				data, err := os.ReadFile(livePath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(livePath, append(data, []byte("{interrupted")...), 0o600))
			case "malformed-legacy":
				writeUIFixture(t, audit.MissionLegacyLogPath(root, id), []byte("{malformed legacy event\n"))
			case "invalid-authority":
				require.NoError(t, os.WriteFile(authorityPath, []byte("version: 2\nmission: "+id+"\nentries: []\n"), 0o600))
			case "truncated-authority":
				require.NoError(t, os.WriteFile(authorityPath, []byte("version: 1\nmission: "+id+"\nentries: ["), 0o600))
			case "missing-authority":
				require.NoError(t, os.Remove(authorityPath))
			case "initialized-empty":
				// A prepared event alone must not invent an accepted annotation.
				require.NoError(t, os.WriteFile(authorityPath, emptyAuthority, 0o600))
			}

			srv, err := NewServer(checkout, root)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = global, filepath.Join(global, "sessions")
			before := uiHistorySnapshot(t, base)
			// Concurrent reads share templates and authority, but must not create
			// recovery state, truncate torn tails or backfill shared records.
			responses := make(chan *httptest.ResponseRecorder, 2)
			for range 2 {
				go func() {
					rec := httptest.NewRecorder()
					srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missions/"+id, nil))
					responses <- rec
				}()
			}
			for range 2 {
				rec := <-responses
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				body := rec.Body.String()
				assert.Contains(t, body, id)
				assert.Equal(t, tc.wantCorrection, strings.Contains(body, ">Corrections</h2>"), body)
				if tc.wantCorrection {
					_, section, ok := strings.Cut(body, ">Corrections</h2>")
					require.True(t, ok, body)
					assert.Contains(t, section, ">Round 1</span>")
					assert.Contains(t, section, `pill-factual`)
					assert.Contains(t, section, "by "+correction.Author)
					assert.Contains(t, section, correction.Claim)
					assert.Contains(t, section, correction.Corrected)
				} else {
					assert.NotContains(t, body, correction.Claim)
					assert.NotContains(t, body, correction.Corrected)
				}
				assert.Equal(t, tc.wantEvents, strings.Contains(body, ">Event Log</h2>"), body)
				if tc.wantEvents {
					assert.Contains(t, body, ">create</span>")
					assert.Contains(t, body, ">correct</span>")
					assert.Contains(t, body, ">"+tc.acceptance+"</span>")
				}
				assert.Equal(t, tc.eventsError != "", strings.Contains(body, "could not load events"), body)
				if tc.eventsError != "" {
					assert.Contains(t, body, tc.eventsError)
				}
				assert.Equal(t, tc.eventsWarning != "", strings.Contains(body, "mission log is partially unreadable"), body)
				if tc.eventsWarning != "" {
					assert.Contains(t, body, tc.eventsWarning)
				}
				assert.Equal(t, tc.correctionError != "", strings.Contains(body, "could not load corrections"), body)
				if tc.correctionError != "" {
					assert.Contains(t, body, tc.correctionError)
				}
				assert.NotContains(t, body, "corrections may be incomplete", "optional event diagnostics must not contaminate shared corrections")
			}
			assert.Equal(t, before, uiHistorySnapshot(t, base), "HTTP reads must leave paths, bytes, modes and modification times unchanged")
		})
	}
}

func TestHandleMissionIndependentHistoryLegacyReadOnly(t *testing.T) {
	root, global := t.TempDir(), t.TempDir()
	id := "m-2026-11-05-201"
	store := mission.NewStoreWithRoots(root, global).WithSessionID("ui-legacy")
	require.NoError(t, store.Create(uiTestContract(id)))
	require.NoError(t, store.AppendResult(id, &mission.Result{
		Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.9,
		Evidence: []mission.EvidenceCheck{{Name: "legacy check", Status: mission.EvidenceStatusPass}},
	}))
	_, err := store.Close(id, mission.StatusClosed)
	require.NoError(t, err)
	c, err := store.Load(id)
	require.NoError(t, err)
	// Represent the supported pre-DES-079 omission explicitly, after creation.
	// Reads may derive old corrections from complete legacy evidence, not persist
	// an upgrade or infer acceptance from a new operation-ID-bearing event.
	c.RoundDecisions = nil
	data, err := yaml.Marshal(c)
	require.NoError(t, err)
	writeUIFixture(t, mission.RepoStatePath(root, "missions", id, "contract.yaml"), data)
	require.NoError(t, os.Remove(mission.RepoStatePath(root, "missions", id, "corrections.yaml")))
	require.NoError(t, os.RemoveAll(audit.LocalZoneBase(root)))
	data, err = json.Marshal(mission.Event{
		TS: "2026-11-05T00:00:00Z", Event: mission.EventCorrect, Actor: "legacy-reviewer",
		Details: map[string]any{"kind": "decision", "round": 0, "corrected": "retained legacy correction"},
	})
	require.NoError(t, err)
	writeUIFixture(t, audit.MissionLegacyLogPath(root, id), append(data, '\n'))
	srv, err := NewServer(root, root)
	require.NoError(t, err)
	srv.globalRoot, srv.globalSessionsDir = global, filepath.Join(global, "sessions")
	before, globalBefore := uiHistorySnapshot(t, root), uiHistorySnapshot(t, global)
	for range 2 {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missions/"+id, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), ">Corrections</h2>")
		assert.Contains(t, rec.Body.String(), "Whole mission")
		assert.Contains(t, rec.Body.String(), "by legacy-reviewer")
		assert.Contains(t, rec.Body.String(), "retained legacy correction")
		assert.Contains(t, rec.Body.String(), ">Event Log</h2>")
		assert.Contains(t, rec.Body.String(), ">correct</span>")
		assert.NotContains(t, rec.Body.String(), "could not load corrections")
		assert.NotContains(t, rec.Body.String(), "could not load events")
		assert.NotContains(t, rec.Body.String(), "partially unreadable")
	}
	assert.Equal(t, before, uiHistorySnapshot(t, root))
	assert.Equal(t, globalBefore, uiHistorySnapshot(t, global))
	assert.NoFileExists(t, mission.RepoStatePath(root, "missions", id, "corrections.yaml"))
	assert.NoDirExists(t, audit.LocalZoneBase(root))
}

type uiHistoryFileState struct {
	Mode     os.FileMode
	Size     int64
	Modified time.Time
	Digest   [sha256.Size]byte
}

func uiHistorySnapshot(t *testing.T, root string) map[string]uiHistoryFileState {
	t.Helper()
	files := make(map[string]uiHistoryFileState)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := uiHistoryFileState{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Digest = sha256.Sum256(data)
		}
		files[path] = state
		return nil
	}))
	return files
}
