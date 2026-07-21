package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// liveEntry appends one entry through appendLiveAudit and returns the
// allocated entry. It resolves the live/sealed paths for a repo+session.
func liveEntry(t *testing.T, repo, sessionID, tool string, now time.Time) auditEntry {
	t.Helper()
	live := liveAuditPath(repo, sessionID)
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sessionID)
	legacy := filepath.Join(sealedDir, "audit.jsonl")
	e := auditEntry{Session: sessionID, Tool: tool}
	got, err := appendLiveAudit(live, sealedDir, legacy, e, now)
	if err != nil {
		t.Fatalf("appendLiveAudit: %v", err)
	}
	return got
}

func TestAppendLiveAuditMonotonic(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-mono"
	// Three appends at the same wall-clock instant must get strictly
	// increasing timestamps.
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a := liveEntry(t, repo, sid, "Read", now)
	b := liveEntry(t, repo, sid, "Read", now)
	c := liveEntry(t, repo, sid, "Read", now)
	ta, _ := audit.ParseLineTS(a.Ts)
	tb, _ := audit.ParseLineTS(b.Ts)
	tc, _ := audit.ParseLineTS(c.Ts)
	if !(ta < tb && tb < tc) {
		t.Errorf("timestamps not strictly increasing: %d, %d, %d", ta, tb, tc)
	}
}

func TestAppendLiveAuditClockRegression(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-regress"
	later := time.Date(2026, 7, 20, 12, 0, 5, 0, time.UTC)
	earlier := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a := liveEntry(t, repo, sid, "Read", later)
	// A backward clock step must still produce a strictly greater ts.
	b := liveEntry(t, repo, sid, "Read", earlier)
	ta, _ := audit.ParseLineTS(a.Ts)
	tb, _ := audit.ParseLineTS(b.Ts)
	if tb <= ta {
		t.Errorf("clock regression not corrected: a=%d b=%d", ta, tb)
	}
	if tb != ta+1 {
		t.Errorf("bumped ts = %d, want %d (last+1ns)", tb, ta+1)
	}
}

func TestAppendLiveAuditRestartRecovery(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-restart"
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	a := liveEntry(t, repo, sid, "Read", now)
	// A fresh call (simulating a process restart) recovers last_ts from
	// the live file tail and continues monotonically at the same instant.
	b := liveEntry(t, repo, sid, "Read", now)
	ta, _ := audit.ParseLineTS(a.Ts)
	tb, _ := audit.ParseLineTS(b.Ts)
	if tb <= ta {
		t.Errorf("restart recovery failed: a=%d b=%d", ta, tb)
	}
}

func TestAppendLiveAuditSeedsAboveWatermark(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-seed"
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sid)
	// A committed chunk already covers a high ts. The first live append
	// must sort strictly after it even though the wall clock is far below.
	high := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(high-10, high), high-10, high)
	past := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	got := liveEntry(t, repo, sid, "Read", past)
	ts, _ := audit.ParseLineTS(got.Ts)
	if ts <= high {
		t.Errorf("live ts %d did not sort above watermark %d", ts, high)
	}
}

func TestAppendLiveAuditTruncatesTornTail(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-torn"
	live := liveAuditPath(repo, sid)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	// A complete line followed by a torn (no-newline) fragment.
	complete := `{"ts":"` + audit.FormatLineTS(1000) + `","session":"s","tool":"Read"}` + "\n"
	torn := `{"ts":"` + audit.FormatLineTS(2000) + `","session":"s","tool":"Bas`
	if err := os.WriteFile(live, []byte(complete+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	liveEntry(t, repo, sid, "Read", now)
	data, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	// The torn fragment must be gone; the file must hold exactly the
	// complete line plus the new append (two lines, both terminated).
	lines := audit.SplitLines(data)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines after torn-tail truncation, got %d: %q", len(lines), data)
	}
	if data[len(data)-1] != '\n' {
		t.Error("file does not end in newline after append")
	}
}

func TestReadSessionAuditUnion(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-union"
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sid)
	// One sealed chunk (ts 100,200) plus live lines past the watermark.
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 200), 100, 200)
	live := liveAuditPath(repo, sid)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"ts":"` + audit.FormatLineTS(200) + `","session":"` + sid + `","tool":"Read"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(300) + `","session":"` + sid + `","tool":"Bash"}` + "\n"
	if err := os.WriteFile(live, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := readSessionAudit(repo, sid, time.Now())
	if err != nil {
		t.Fatalf("readSessionAudit: %v", err)
	}
	// Sealed 100,200 + live tail past watermark 200 → only ts 300 added.
	// The live line at ts 200 is at the watermark, not past it.
	if len(entries) != 3 {
		t.Fatalf("union entries = %d, want 3 (100,200 sealed + 300 live): %+v", len(entries), entries)
	}
	var last int64
	for _, e := range entries {
		ts, _ := audit.ParseLineTS(e.Ts)
		if ts < last {
			t.Errorf("entries not ordered by ts: %d after %d", ts, last)
		}
		last = ts
	}
}

func TestReadSessionAuditCorruptChunkErrors(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-corrupt"
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sid)
	// A chunk whose last line ts (150) disagrees with its filename <last> (200).
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 200), 100, 150)
	_, err := readSessionAudit(repo, sid, time.Now())
	if err == nil {
		t.Fatal("readSessionAudit over content-vs-name mismatch = nil, want error")
	}
}

func TestReadSessionAuditCrossBranchOverlapDedup(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-overlap"
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sid)
	// Two chunks whose ranges overlap on 100..200 — the shape a
	// cross-branch re-seal leaves after both branches merge. The wider
	// chunk (100..300) re-sealed lines the narrower (100..200) already had.
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 200), 100, 200)
	writeChunkFile(t, sealedDir, audit.SessionChunkFile(100, 300), 100, 200, 300)
	entries, err := readSessionAudit(repo, sid, time.Now())
	if err != nil {
		t.Fatalf("readSessionAudit: %v", err)
	}
	// (session, ts) dedup collapses the overlap: distinct ts are 100,200,300.
	if len(entries) != 3 {
		t.Fatalf("overlap dedup: got %d entries, want 3 (100,200,300): %+v", len(entries), entries)
	}
	seen := map[int64]bool{}
	for _, e := range entries {
		ts, _ := audit.ParseLineTS(e.Ts)
		if seen[ts] {
			t.Errorf("duplicate ts %d survived dedup", ts)
		}
		seen[ts] = true
	}
}

func TestReadSessionAuditLegacyOnly(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-legacy"
	sealedDir := filepath.Join(sealedSessionsBase(repo), "2026-01-01-"+sid)
	// A frozen legacy audit.jsonl with two lines, no chunks, no live file.
	writeChunkFile(t, sealedDir, "audit.jsonl", 100, 200)
	entries, err := readSessionAudit(repo, sid, time.Now())
	if err != nil {
		t.Fatalf("readSessionAudit legacy-only: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("legacy-only entries = %d, want 2", len(entries))
	}
}
