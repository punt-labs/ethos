package audit

import (
	"os"
	"path/filepath"
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
	got, err := ListTombstones(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("ListTombstones = %d, want 2", len(got))
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
