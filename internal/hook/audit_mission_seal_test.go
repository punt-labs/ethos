package hook

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// writeMissionLiveLines writes ts event lines to a (mission, session) live log.
func writeMissionLiveLines(t *testing.T, repo, missionID, sessionID string, tss ...int64) {
	t.Helper()
	live := liveMissionLogPath(repo, missionID, sessionID)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		body = append(body, []byte(`{"ts":"`+audit.FormatLineTS(ts)+`","event":"update","actor":"claude"}`+"\n")...)
	}
	if err := os.WriteFile(live, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSealMissionWritesChunk(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	mid := "m-2026-07-21-001"
	writeMissionLiveLines(t, repo, mid, "sessA", 100, 200)
	writeMissionLiveLines(t, repo, mid, "sessB", 300, 400)

	now := time.Now().UTC()
	res, err := SealMission(repo, mid, now, SealOptions{})
	if err != nil {
		t.Fatalf("SealMission: %v", err)
	}
	if res.LinesSealed != 4 || res.SessionsSealed != 2 {
		t.Errorf("res = %+v, want 4 lines / 2 sessions", res)
	}
	sealedDir := sealedMissionDir(repo, mid)
	// Each session seals into its own log-<session>-* chunk.
	if _, err := os.Stat(filepath.Join(sealedDir, audit.MissionChunkFile("sessA", 100, 200))); err != nil {
		t.Errorf("sessA chunk missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, audit.MissionChunkFile("sessB", 300, 400))); err != nil {
		t.Errorf("sessB chunk missing: %v", err)
	}
}

// TestSealMissionConcurrentSessions seals one mission from two sessions at
// once, each under its own per-(mission, session) flock into the shared
// mission directory. Neither seal may spuriously fail by sweeping the other's
// in-flight temp: both chunks must land. It drives the seal critical section
// directly (where SweepStaleTemps + WriteChunkAtomic run) rather than the full
// sealMissionSession, so the two goroutines do not contend on git's single
// index.lock — a git-level concern orthogonal to the sweep race under test.
// Run under -race.
func TestSealMissionConcurrentSessions(t *testing.T) {
	repo := t.TempDir()
	mid := "m-2026-07-21-010"
	// Many lines per session widen each WriteChunkAtomic window, so the two
	// sweeps and writes are more likely to interleave.
	tss := make([]int64, 0, 200)
	for i := int64(1); i <= 200; i++ {
		tss = append(tss, i)
	}
	writeMissionLiveLines(t, repo, mid, "sessA", tss...)
	writeMissionLiveLines(t, repo, mid, "sessB", tss...)

	now := time.Now().UTC()
	sealedDir := sealedMissionDir(repo, mid)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	seal := func(idx int, sessionID string) {
		defer wg.Done()
		errs[idx] = WithLiveMissionLock(repo, mid, sessionID, func() error {
			_, e := sealDirLocked(sealDirParams{
				repoRoot:  repo,
				ns:        audit.MissionNS,
				session:   sessionID,
				sealedDir: sealedDir,
				livePath:  liveMissionLogPath(repo, mid, sessionID),
				chunkName: func(f, l int64) string { return audit.MissionChunkFile(sessionID, f, l) },
				tempName:  func(f, l int64) string { return audit.MissionTempFile(sessionID, f, l) },
				label:     "mission " + mid + " session " + sessionID,
			}, now, SealOptions{})
			return e
		})
	}
	wg.Add(2)
	go seal(0, "sessA")
	go seal(1, "sessB")
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent seal %d failed: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sealedDir, audit.MissionChunkFile("sessA", 1, 200))); err != nil {
		t.Errorf("sessA chunk missing after concurrent seal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, audit.MissionChunkFile("sessB", 1, 200))); err != nil {
		t.Errorf("sessB chunk missing after concurrent seal: %v", err)
	}
}

func TestSealMissionIdempotent(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	mid := "m-2026-07-21-002"
	writeMissionLiveLines(t, repo, mid, "sessA", 100, 200)
	now := time.Now().UTC()
	if _, err := SealMission(repo, mid, now, SealOptions{}); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	res, err := SealMission(repo, mid, now, SealOptions{})
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if res.SessionsSealed != 0 || res.LinesSealed != 0 {
		t.Errorf("second seal sealed something: %+v", res)
	}
}

func TestSealRepoSealsMissionsToo(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	// A session audit line and a mission log line both pending.
	writeLiveLines(t, repo, "sess-audit", 100, 200)
	writeMissionLiveLines(t, repo, "m-2026-07-21-003", "sess-audit", 500, 600)
	res, err := SealRepo(repo, time.Now().UTC(), SealOptions{})
	if err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	// 2 audit + 2 mission lines across 2 units.
	if res.LinesSealed != 4 || res.SessionsSealed != 2 {
		t.Errorf("res = %+v, want 4 lines / 2 units", res)
	}
}
