package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// writeLiveLines writes ts lines to a session's live audit file.
func writeLiveLines(t *testing.T, repo, sessionID string, tss ...int64) {
	t.Helper()
	live := liveAuditPath(repo, sessionID)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		body = append(body, []byte(`{"ts":"`+audit.FormatLineTS(ts)+`","session":"`+sessionID+`","tool":"Read"}`+"\n")...)
	}
	if err := os.WriteFile(live, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSealRepoDatesDirBySessionStart(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-dated"
	// The live file's first line is dated 2026-07-15; the seal runs on
	// 2026-07-20. The brand-new sealed dir must carry the session's first-write
	// date, not the wall-clock seal date (carried refinement (a)).
	firstTS := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC).UnixNano()
	writeLiveLines(t, repo, sid, firstTS, firstTS+1)
	sealNow := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if _, err := SealRepo(repo, sealNow, SealOptions{}); err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	dir, err := audit.FindSealedSessionDir(repo, sid)
	if err != nil || dir == "" {
		t.Fatalf("no sealed dir: %v", err)
	}
	if got := filepath.Base(dir); got != "2026-07-15-"+sid {
		t.Errorf("sealed dir = %q, want 2026-07-15-%s (first-line date, not seal date)", got, sid)
	}
}

func TestSealRepoStartDateOverridesLiveDate(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-roster"
	firstTS := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC).UnixNano()
	writeLiveLines(t, repo, sid, firstTS)
	// An authoritative roster start date wins over the live first-line date.
	opts := SealOptions{StartDate: func(string) string { return "2026-07-10" }}
	if _, err := SealRepo(repo, time.Now().UTC(), opts); err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	dir, _ := audit.FindSealedSessionDir(repo, sid)
	if got := filepath.Base(dir); got != "2026-07-10-"+sid {
		t.Errorf("sealed dir = %q, want 2026-07-10-%s (roster start date)", got, sid)
	}
}

func TestSealRepoWritesChunk(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-seal"
	writeLiveLines(t, repo, sid, 100, 200, 300)

	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	res, err := SealRepo(repo, now, SealOptions{})
	if err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	if res.LinesSealed != 3 || res.SessionsSealed != 1 {
		t.Errorf("res = %+v, want 3 lines / 1 session", res)
	}
	// The chunk exists and is named for the range.
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	chunk := filepath.Join(sealedDir, audit.SessionChunkFile(100, 300))
	if _, err := os.Stat(chunk); err != nil {
		t.Fatalf("expected chunk %s: %v", chunk, err)
	}
}

func TestSealRepoIdempotent(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-idem"
	writeLiveLines(t, repo, sid, 100, 200)
	now := time.Now().UTC()
	if _, err := SealRepo(repo, now, SealOptions{}); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	// Second seal: nothing new past the watermark, so no new chunk.
	res, err := SealRepo(repo, now, SealOptions{})
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if res.SessionsSealed != 0 || res.LinesSealed != 0 {
		t.Errorf("second seal sealed something: %+v", res)
	}
}

func TestSealRepoIncrementalTail(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-incr"
	writeLiveLines(t, repo, sid, 100, 200)
	now := time.Now().UTC()
	if _, err := SealRepo(repo, now, SealOptions{}); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	// Append more live lines past the first watermark, then re-seal.
	writeLiveLines(t, repo, sid, 100, 200, 300, 400)
	res, err := SealRepo(repo, now, SealOptions{})
	if err != nil {
		t.Fatalf("second seal: %v", err)
	}
	if res.LinesSealed != 2 {
		t.Errorf("incremental seal = %d lines, want 2 (300,400)", res.LinesSealed)
	}
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	if _, err := os.Stat(filepath.Join(sealedDir, audit.SessionChunkFile(300, 400))); err != nil {
		t.Errorf("expected second chunk 300-400: %v", err)
	}
}

func TestSealRepoStagesOrphanChunk(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-orphan"
	now := time.Now().UTC()
	// Simulate a crash after rename but before staging: a complete,
	// untracked chunk with no pending live lines.
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 200), 100, 200)

	res, err := SealRepo(repo, now, SealOptions{})
	if err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	if res.ChunksStaged < 1 {
		t.Errorf("orphan chunk not staged: %+v", res)
	}
	// The chunk is now tracked (git status shows it staged, not untracked).
	cmd := exec.Command("git", "status", "--porcelain", "--", filepath.Join(sealedDir, audit.SessionChunkFile(100, 200)))
	cmd.Dir = repo
	out, _ := cmd.Output()
	if len(out) > 0 && out[0] == '?' {
		t.Errorf("chunk still untracked after seal: %s", out)
	}
}

func TestSealRepoDryRun(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-dry"
	writeLiveLines(t, repo, sid, 100, 200)
	now := time.Now().UTC()
	res, err := SealRepo(repo, now, SealOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry-run seal: %v", err)
	}
	if res.LinesSealed != 2 {
		t.Errorf("dry-run pending = %d, want 2", res.LinesSealed)
	}
	// No chunk written.
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	if _, err := os.Stat(filepath.Join(sealedDir, audit.SessionChunkFile(100, 200))); err == nil {
		t.Error("dry-run wrote a chunk")
	}
}

func TestSealRepoCorruptChunkFailsClosed(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-bad"
	now := time.Now().UTC()
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	// A chunk whose last ts (150) disagrees with its filename <last> (200).
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 200), 100, 150)
	writeLiveLines(t, repo, sid, 300)
	_, err := SealRepo(repo, now, SealOptions{})
	if err == nil {
		t.Fatal("SealRepo over corrupt chunk = nil, want fail-closed error")
	}
}

func TestSealRepoSweepsStaleTemp(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-temp"
	now := time.Now().UTC()
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A stale temp from a crashed seal.
	stale := filepath.Join(sealedDir, audit.SessionTempFile(100, 150))
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLiveLines(t, repo, sid, 100, 200)
	if _, err := SealRepo(repo, now, SealOptions{}); err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp not swept: %v", err)
	}
}

func TestSealThenReadIdentical(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-e2e"
	now := time.Now().UTC()
	writeLiveLines(t, repo, sid, 100, 200, 300)

	before, err := readSessionAudit(repo, sid, now)
	if err != nil {
		t.Fatalf("read before seal: %v", err)
	}
	if _, err := SealRepo(repo, now, SealOptions{}); err != nil {
		t.Fatalf("SealRepo: %v", err)
	}
	after, err := readSessionAudit(repo, sid, now)
	if err != nil {
		t.Fatalf("read after seal: %v", err)
	}
	if err := auditEntriesEqual(before, after); err != nil {
		t.Errorf("read differs across seal: %v", err)
	}
}
