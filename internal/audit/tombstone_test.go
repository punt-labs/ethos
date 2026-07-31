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

	got, err := ExpectedMissionLiveFiles(repo, repo, "sess1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MissionID != mid {
		t.Fatalf("expected one mission live for sess1, got %+v", got)
	}
	// The live file is absent here, but the chunk holds the lines: not loss.
	if got[0].Present {
		t.Error("expected mission live file reported Present, want absent")
	}
	if !got[0].Sealed {
		t.Error("Sealed = false beside a tracked chunk carrying the session")
	}
	if got[0].Lost() {
		t.Error("Lost() = true for an absent live file whose lines are sealed")
	}
	// Once the live file exists, Present flips true.
	if err := os.MkdirAll(filepath.Dir(got[0].LivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(got[0].LivePath, []byte(`{"ts":"`+FormatLineTS(300)+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ExpectedMissionLiveFiles(repo, repo, "sess1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Present {
		t.Error("expected mission live file reported absent after write")
	}
	// It holds one line past the empty watermark → unsealed.
	n, err := MissionUnsealedCount(repo, repo, mid, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("mission unsealed count = %d, want 1", n)
	}
}

// TestExpectedMissionLiveFilesLost covers the ethos-q6e2 predicate: absence of
// a per-checkout live file is loss only when nothing durable holds the lines.
func TestExpectedMissionLiveFilesLost(t *testing.T) {
	const sess = "sess1"
	cases := []struct {
		name    string
		mission string
		setup   func(t *testing.T, repo, mid string)
		bound   []string
		// writerGone probes a recorded writer checkout that no longer exists,
		// rather than the checkout the fixture was built in.
		writerGone bool
		wantLost   bool
	}{
		{
			name:    "sealed chunk, no live file, writer never wrote mission logs",
			mission: "m-2026-07-21-001",
			setup: func(t *testing.T, repo, mid string) {
				writeChunk(t, SealedMissionDir(repo, mid), MissionChunkFile(sess, 100, 200), 100, 200)
			},
			// The writer is present and has no live-missions zone at all, so
			// its missing file is the ordinary absence of another checkout's
			// state. This is the 43-warning case.
			wantLost: false,
		},
		{
			name:    "bound, sealed nothing, no live file",
			mission: "m-2026-07-21-002",
			setup:   func(*testing.T, string, string) {},
			bound:   []string{"m-2026-07-21-002"},
			// The genuine loss: the session claimed the mission, sealed no
			// chunk, and its live log is gone.
			wantLost: true,
		},
		{
			name:    "bound, sealed nothing, live file present",
			mission: "m-2026-07-21-003",
			setup: func(t *testing.T, repo, mid string) {
				writeLiveMissionLine(t, repo, mid, sess, 100)
			},
			bound:    []string{"m-2026-07-21-003"},
			wantLost: false,
		},
		{
			name:    "bound, pre-DES-058 legacy log.jsonl, no live file",
			mission: "m-2026-07-20-013",
			setup: func(t *testing.T, repo, mid string) {
				writeLegacyMissionLog(t, repo, mid, 100)
			},
			bound:    []string{"m-2026-07-20-013"},
			wantLost: false,
		},
		{
			name:    "bound, only another session's chunk, no live file",
			mission: "m-2026-07-21-004",
			setup: func(t *testing.T, repo, mid string) {
				writeChunk(t, SealedMissionDir(repo, mid), MissionChunkFile("other", 100, 200), 100, 200)
			},
			bound: []string{"m-2026-07-21-004"},
			// Another session's chunk says nothing about this session's lines.
			wantLost: true,
		},
		{
			name:    "bound, hybrid mission: legacy log plus another session's chunk",
			mission: "m-2026-07-20-020",
			setup: func(t *testing.T, repo, mid string) {
				writeLegacyMissionLog(t, repo, mid, 100)
				writeChunk(t, SealedMissionDir(repo, mid), MissionChunkFile("other", 200, 300), 200, 300)
			},
			bound: []string{"m-2026-07-20-020"},
			// A mission worked on both sides of the split. Its frozen log
			// predates per-session attribution, so it cannot vouch for this
			// session — whose own live log is gone and sealed nowhere.
			wantLost: true,
		},
		{
			name:    "sealed chunk, writer checkout deleted entirely",
			mission: "m-2026-07-21-007",
			setup: func(t *testing.T, repo, mid string) {
				writeChunk(t, SealedMissionDir(repo, mid), MissionChunkFile(sess, 100, 200), 100, 200)
			},
			// The recorded writer is gone, so its absent live zone is not
			// evidence it never wrote — it is the crash -> checkout-deleted
			// case, and the post-watermark tail went with it.
			writerGone: true,
			wantLost:   true,
		},
		{
			name:    "sealed chunk, live log DELETED in the writer's own checkout",
			mission: "m-2026-07-21-005",
			setup: func(t *testing.T, repo, mid string) {
				writeChunk(t, SealedMissionDir(repo, mid), MissionChunkFile(sess, 100, 200), 100, 200)
				// The checkout has a live-missions zone — another mission's
				// live log stands — so this mission's absent file is a
				// deletion, not the ordinary absence of another checkout's
				// files. The chunk vouches only through ts 200; anything
				// written after it lived solely in the deleted file.
				writeLiveMissionLine(t, repo, "m-2026-07-21-006", sess, 300)
			},
			wantLost: true,
		},
		{
			name:    "bound, hybrid mission whose chunks were all quarantined",
			mission: "m-2026-07-20-021",
			setup: func(t *testing.T, repo, mid string) {
				writeLegacyMissionLog(t, repo, mid, 100)
				// Quarantine retires the chunk and leaves a marker, so no
				// KindValid chunk survives — but the mission was still worked
				// after the split and its legacy log must not vouch for anyone.
				dir := SealedMissionDir(repo, mid)
				cn, _ := Classify(MissionChunkFile("other", 200, 300), MissionNS)
				writeChunk(t, dir, cn.ChunkFile()+".corrupt", 200, 300)
				writeChunk(t, dir, cn.MarkerFile())
			},
			bound:    []string{"m-2026-07-20-021"},
			wantLost: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			tc.setup(t, repo, tc.mission)
			liveRoot := repo
			if tc.writerGone {
				liveRoot = filepath.Join(t.TempDir(), "deleted-checkout")
			}
			got, err := ExpectedMissionLiveFiles(repo, liveRoot, sess, tc.bound)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].MissionID != tc.mission {
				t.Fatalf("expected one entry for %s, got %+v", tc.mission, got)
			}
			if got[0].Lost() != tc.wantLost {
				t.Errorf("Lost() = %v, want %v (%+v)", got[0].Lost(), tc.wantLost, got[0])
			}
		})
	}
}

// writeLiveMissionLine appends one line to a mission's per-(mission, session)
// live log in the checkout's local zone.
func writeLiveMissionLine(t *testing.T, repo, mid, sess string, ts int64) {
	t.Helper()
	path := LiveMissionLogPath(repo, mid, sess)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + FormatLineTS(ts) + `","tool":"Bash"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLegacyMissionLog writes the frozen pre-DES-058 tracked log.jsonl a
// mission closed before the live/sealed split carries instead of chunks.
func writeLegacyMissionLog(t *testing.T, repo, mid string, ts int64) {
	t.Helper()
	dir := SealedMissionDir(repo, mid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"` + FormatLineTS(ts) + `","event":"dispatch"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "log.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}
