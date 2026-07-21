package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTombstoneRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tb := Tombstone{
		Session: "sess1", StartDate: "2026-07-21", Repo: "/repo", Checkout: "/repo",
		UnsealedLines: true,
	}
	if err := WriteTombstone(dir, tb); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTombstone(filepath.Join(dir, "sess1.purged"))
	if err != nil {
		t.Fatal(err)
	}
	if got != tb {
		t.Errorf("round-trip tombstone = %+v, want %+v", got, tb)
	}
	if !got.Flagged() {
		t.Error("Flagged() = false, want true")
	}
}

func TestListTombstones(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTombstone(dir, Tombstone{Session: "a", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	if err := WriteTombstone(dir, Tombstone{Session: "b", Repo: "/r", LiveFileGone: true}); err != nil {
		t.Fatal(err)
	}
	got, err := ListTombstones(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("ListTombstones = %d, want 2", len(got))
	}
}

func TestListTombstonesCountsTorn(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTombstone(dir, Tombstone{Session: "good", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	// A torn tombstone: valid name, undecodable content. It reads as absent but
	// its loss must not vanish silently — ListTombstones counts it on warn.
	if err := os.WriteFile(filepath.Join(dir, "torn.purged"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	got, err := ListTombstones(dir, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("live tombstones = %d, want 1", len(got))
	}
	if !strings.Contains(warn.String(), "skipped 1 torn tombstone") {
		t.Errorf("torn tombstone not reported: %q", warn.String())
	}
}

func TestWriteTombstoneAtomicLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("atomic write left a temp file: %s", e.Name())
		}
	}
	// The tombstone still reads back cleanly.
	if _, err := ReadTombstone(filepath.Join(dir, "s.purged")); err != nil {
		t.Errorf("tombstone unreadable after atomic write: %v", err)
	}
}

func TestAckTombstoneNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	// First purge+ack: retires to .purged.acked.
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	name1, err := AckTombstone(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if name1 != "s.purged.acked" {
		t.Errorf("first ack retired to %q, want s.purged.acked", name1)
	}
	// Re-purge (a forced-purge re-registered the id) with different content,
	// then ack again: must NOT overwrite the first record.
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", LiveFileGone: true}); err != nil {
		t.Fatal(err)
	}
	name2, err := AckTombstone(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	if name2 == name1 {
		t.Fatalf("second ack reused the first name %q — overwrote the loss record", name2)
	}
	// Both retired records survive on disk.
	if _, err := os.Stat(filepath.Join(dir, name1)); err != nil {
		t.Errorf("first acked record lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name2)); err != nil {
		t.Errorf("second acked record lost: %v", err)
	}
}

func TestWriteTombstoneRetiresFlaggedPrior(t *testing.T) {
	dir := t.TempDir()
	// First flagged tombstone.
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	// A re-purge before an ack must NOT overwrite the first loss record.
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", LiveFileGone: true}); err != nil {
		t.Fatal(err)
	}
	// The fresh tombstone stands at the name.
	fresh, err := ReadTombstone(filepath.Join(dir, "s.purged"))
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.LiveFileGone {
		t.Errorf("fresh tombstone = %+v, want LiveFileGone", fresh)
	}
	// The prior flagged tombstone was retired, not dropped.
	if _, err := os.Stat(filepath.Join(dir, "s.purged.acked")); err != nil {
		t.Errorf("prior flagged tombstone not retired to .acked: %v", err)
	}
}

func TestWriteTombstoneReplacesUnflaggedPrior(t *testing.T) {
	dir := t.TempDir()
	// An unflagged tombstone carries no loss signal, so it may be replaced.
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteTombstone(dir, Tombstone{Session: "s", Repo: "/r", UnsealedLines: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.purged.acked")); !os.IsNotExist(err) {
		t.Errorf("unflagged prior should be replaced, not retired: %v", err)
	}
}

func TestAckTombstoneMissing(t *testing.T) {
	if _, err := AckTombstone(t.TempDir(), "nope"); err == nil {
		t.Error("acking a missing tombstone = nil error, want error")
	}
}

func TestSessionUnsealedCount(t *testing.T) {
	repo := t.TempDir()
	sid := "sess-count"
	live := LiveAuditPath(repo, sid)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n" + `{"ts":"` + FormatLineTS(200) + `","tool":"Read"}` + "\n"
	if err := os.WriteFile(live, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// No sealed chunk → watermark 0 → both lines unsealed.
	n, err := SessionUnsealedCount(repo, sid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("unsealed count = %d, want 2", n)
	}
}

func TestExpectedMissionLiveFiles(t *testing.T) {
	repo := t.TempDir()
	mid := "m-2026-07-21-001"
	dir := SealedMissionDir(repo, mid)
	// A tracked chunk carrying sess1 proves sess1 wrote the mission live log.
	writeChunk(t, dir, MissionChunkFile("sess1", 100, 200), 100, 200)
	// A chunk for a different session must not appear for sess1.
	writeChunk(t, dir, MissionChunkFile("sess2", 300, 400), 300, 400)

	got, err := ExpectedMissionLiveFiles(repo, "sess1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MissionID != mid {
		t.Fatalf("expected one mission live for sess1, got %+v", got)
	}
	// The live file was never written → absent → evidence of loss.
	if got[0].Present {
		t.Error("expected mission live file reported Present, want absent")
	}
	// Once the live file exists, Present flips true.
	if err := os.MkdirAll(filepath.Dir(got[0].LivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(got[0].LivePath, []byte(`{"ts":"`+FormatLineTS(300)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ExpectedMissionLiveFiles(repo, "sess1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Present {
		t.Error("expected mission live file reported absent after write")
	}
	// It holds one line past the empty watermark → unsealed.
	n, err := MissionUnsealedCount(repo, mid, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("mission unsealed count = %d, want 1", n)
	}
}
