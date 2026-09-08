package audit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errSimulatedFsync is the injected error for fsyncFile overrides in
// the AppendMonotonic sync-failure tests below.
var errSimulatedFsync = errors.New("simulated fsync failure")

// errSimulatedWrite is the injected error for writeFile overrides in the
// AppendMonotonic write-failure tests below.
var errSimulatedWrite = errors.New("simulated write failure")

// writeChunk creates a JSONL file holding one line per timestamp.
func writeChunk(t *testing.T, dir, name string, tss ...int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		body = append(body, []byte(`{"ts":"`+FormatLineTS(ts)+`","tool":"Bash"}`+"\n")...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWatermarkEmptyDir(t *testing.T) {
	wm, err := Watermark(filepath.Join(t.TempDir(), "nope"), SessionNS, "")
	if err != nil || wm != 0 {
		t.Fatalf("Watermark on missing dir = %d, %v; want 0, nil", wm, err)
	}
}

func TestWatermarkMaxChunkLast(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	writeChunk(t, dir, SessionChunkFile(201, 350), 201, 350)
	wm, err := Watermark(dir, SessionNS, "")
	if err != nil || wm != 350 {
		t.Fatalf("Watermark = %d, %v; want 350", wm, err)
	}
}

// TestWatermarkExcludesLegacy pins the split: Watermark is the sealed-chunk
// boundary and must NOT fold the frozen legacy file's max ts in — only
// MonotonicFloor does.
func TestWatermarkExcludesLegacy(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	writeChunk(t, dir, "audit.jsonl", 50, 500, 300)
	wm, err := Watermark(dir, SessionNS, "")
	if err != nil || wm != 200 {
		t.Fatalf("Watermark = %d, %v; want 200 (chunks only, legacy excluded)", wm, err)
	}
}

// TestMonotonicFloorIncludesLegacy pins the other half: the append floor folds
// the frozen legacy max in so a new line sorts after pre-discipline history.
func TestMonotonicFloorIncludesLegacy(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	legacy := filepath.Join(dir, "audit.jsonl")
	writeChunk(t, dir, "audit.jsonl", 50, 500, 300)
	fl, err := MonotonicFloor(dir, SessionNS, "", legacy)
	if err != nil || fl != 500 {
		t.Fatalf("MonotonicFloor = %d, %v; want 500 (legacy max)", fl, err)
	}
}

func TestScanNearMissFails(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, "audit-100-200.jsonl", 100)
	if _, err := ScanSealedDir(dir, SessionNS, ""); err == nil {
		t.Fatal("near-miss = nil error, want error")
	}
}

func TestScanOrphanCorruptFails(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200)+".corrupt", 100, 200)
	if _, err := ScanSealedDir(dir, SessionNS, ""); err == nil {
		t.Fatal("orphan .corrupt = nil error, want error")
	}
}

func TestScanCorruptUnderMarkerOK(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200)+".corrupt", 100, 200)
	m := Marker{Chunk: "audit-" + TSToField(100) + "-" + TSToField(200), VerifiedLast: 200, Reason: "test"}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-"+TSToField(100)+"-"+TSToField(200)+".quarantine"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := ScanSealedDir(dir, SessionNS, "")
	if err != nil {
		t.Fatalf("scan with covering marker: %v", err)
	}
	if len(sc.Markers) != 1 {
		t.Errorf("markers = %d, want 1", len(sc.Markers))
	}
}

func TestScanTornMarkerLeavesOrphanCorrupt(t *testing.T) {
	dir := t.TempDir()
	// A .corrupt covered by a marker whose NAME is valid but whose CONTENT is
	// garbage — the marker must read as absent, leaving the .corrupt an
	// uncovered orphan (exit 2), not a silent pass (REQ-2, rsc repro).
	writeChunk(t, dir, "audit-"+TSToField(100)+"-"+TSToField(200)+".jsonl.corrupt", 100, 200)
	markerName := "audit-" + TSToField(100) + "-" + TSToField(200) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte("GARBAGE NOT JSON\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSealedDir(dir, SessionNS, ""); err == nil {
		t.Fatal("garbage-content marker over a .corrupt = nil error, want orphan exit-2")
	}
}

func TestScanParseableMarkerCoversCorrupt(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, "audit-"+TSToField(100)+"-"+TSToField(200)+".jsonl.corrupt", 100, 200)
	m := Marker{Chunk: "audit-" + TSToField(100) + "-" + TSToField(200), VerifiedLast: 200, Reason: "test"}
	data, _ := MarshalMarker(m)
	markerName := "audit-" + TSToField(100) + "-" + TSToField(200) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSealedDir(dir, SessionNS, ""); err != nil {
		t.Errorf("parseable marker should cover its .corrupt: %v", err)
	}
}

// TestScanCrossSessionMarkerDoesNotCoverCorrupt is B1: a mission union read
// (empty session filter) must not let one session's marker cover another
// session's orphan .corrupt. A range-only coverage check would silence the
// fail-loud incomplete-quarantine guard for the second session.
func TestScanCrossSessionMarkerDoesNotCoverCorrupt(t *testing.T) {
	dir := t.TempDir()
	// Session B's retired chunk, no marker of its own.
	writeChunk(t, dir, MissionChunkFile("sessB", 100, 200)+".corrupt", 100, 200)
	// Session A's marker over the identical range — covers A, not B.
	m := Marker{Chunk: "log-sessA-" + TSToField(100) + "-" + TSToField(200), VerifiedLast: 200, Reason: "test"}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	markerName := "log-sessA-" + TSToField(100) + "-" + TSToField(200) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSealedDir(dir, MissionNS, ""); err == nil {
		t.Fatal("cross-session marker covered another session's .corrupt = nil error, want orphan exit-2")
	}
}

// TestScanSameSessionMarkerCoversCorrupt is B1's companion: a marker and a
// .corrupt of the SAME session still cover as before.
func TestScanSameSessionMarkerCoversCorrupt(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, MissionChunkFile("sessA", 100, 200)+".corrupt", 100, 200)
	m := Marker{Chunk: "log-sessA-" + TSToField(100) + "-" + TSToField(200), VerifiedLast: 200, Reason: "test"}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	markerName := "log-sessA-" + TSToField(100) + "-" + TSToField(200) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSealedDir(dir, MissionNS, ""); err != nil {
		t.Errorf("same-session marker should cover its .corrupt: %v", err)
	}
}

func TestScanMissionSessionFilter(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, MissionChunkFile("sessA", 100, 200), 100, 200)
	writeChunk(t, dir, MissionChunkFile("sessB", 300, 400), 300, 400)
	sc, err := ScanSealedDir(dir, MissionNS, "sessA")
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Chunks) != 1 || sc.Chunks[0].Session != "sessA" {
		t.Errorf("session filter failed: %+v", sc.Chunks)
	}
}

func TestMarkerVerifiedLastOverFilename(t *testing.T) {
	dir := t.TempDir()
	m := Marker{Chunk: "audit-" + TSToField(100) + "-" + TSToField(999), VerifiedLast: 200, Reason: "test"}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	writeChunk(t, dir, "audit-"+TSToField(100)+"-"+TSToField(999)+".jsonl.corrupt", 100, 200)
	if err := os.WriteFile(filepath.Join(dir, "audit-"+TSToField(100)+"-"+TSToField(999)+".quarantine"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	wm, err := Watermark(dir, SessionNS, "")
	if err != nil || wm != 200 {
		t.Fatalf("Watermark = %d, %v; want 200 (verified, not filename 999)", wm, err)
	}
}

func TestReadMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := Marker{Chunk: "c", VerifiedLast: 150, UnrecoveredFirst: 151, UnrecoveredLast: 200, Reason: "parse failure"}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "m.quarantine")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMarker(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != m || !got.HasGap() {
		t.Errorf("round-trip marker = %+v", got)
	}
}

func TestGapMarkers(t *testing.T) {
	dir := t.TempDir()
	// A quarantine marker recording an unrecovered sub-range, with its
	// covering .corrupt so the scan is clean.
	writeChunk(t, dir, "audit-"+TSToField(100)+"-"+TSToField(300)+".jsonl.corrupt", 100, 200)
	m := Marker{
		Chunk: "audit-" + TSToField(100) + "-" + TSToField(300), VerifiedLast: 200,
		UnrecoveredFirst: 201, UnrecoveredLast: 300, Reason: "parse failure",
	}
	data, err := MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit-"+TSToField(100)+"-"+TSToField(300)+".quarantine"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	gaps, err := GapMarkers(dir, SessionNS, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].First != 201 || gaps[0].Last != 300 {
		t.Errorf("gaps = %+v, want one [201,300]", gaps)
	}
}

func TestGapMarkersNoneWhenFullRecovery(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, "audit-"+TSToField(100)+"-"+TSToField(200)+".jsonl.corrupt", 100, 200)
	m := Marker{Chunk: "audit-" + TSToField(100) + "-" + TSToField(200), VerifiedLast: 200, Reason: "full recovery"}
	data, _ := MarshalMarker(m)
	if err := os.WriteFile(filepath.Join(dir, "audit-"+TSToField(100)+"-"+TSToField(200)+".quarantine"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	gaps, err := GapMarkers(dir, SessionNS, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Errorf("gaps = %+v, want none (full recovery)", gaps)
	}
}

func TestAppendMonotonicStrictlyIncreasing(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mk := func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	}
	a, _ := AppendMonotonic(live, 0, now, mk)
	b, _ := AppendMonotonic(live, 0, now, mk)
	c, _ := AppendMonotonic(live, 0, now, mk)
	if !(a < b && b < c) {
		t.Errorf("not strictly increasing: %d %d %d", a, b, c)
	}
}

func TestAppendMonotonicSeedsAboveWatermark(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	ts, _ := AppendMonotonic(live, 5000, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if ts <= 5000 {
		t.Errorf("ts %d did not sort above watermark 5000", ts)
	}
}

// TestAppendMonotonic_SyncFailureTruncatesBack pins the leader's PR #509
// tail-round finding: a fully-successful Write followed by a failing
// Sync must not leave the line on disk. Callers of AppendMonotonic
// (internal/mission's appendLiveEventLocked, and through it
// Store.DisclaimDelegation's own rollback-on-append-failure) treat this
// function's error as proof nothing new persisted; before this fix that
// was false for exactly this failure shape, because Sync failing does
// not undo an already-successful Write, and nothing truncated the file
// back.
//
// fsyncFile is a package var for the same reason
// internal/mission/syncdir_unix.go's syncDir is: a real fsync failure
// is not something a portable test can engineer directly.
func TestAppendMonotonic_SyncFailureTruncatesBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	orig := fsyncFile
	t.Cleanup(func() { fsyncFile = orig })
	fsyncFile = func(f *os.File) error {
		return errSimulatedFsync
	}

	_, err := AppendMonotonic(live, 0, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if err == nil {
		t.Fatal("expected an error from the simulated sync failure")
	}

	data, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("reading live file after failed append: %v", readErr)
	}
	if len(data) != 0 {
		t.Errorf("a failed sync must leave no persisted line behind, got %q", data)
	}
}

// TestAppendMonotonic_SyncFailureThenSuccessAppendsExactlyOnce is the
// end-to-end half of the same fix: a retry after a sync failure must
// produce exactly one line, not two — proving the truncate-back
// genuinely removed the failed attempt rather than merely reporting an
// error while leaving it in place.
func TestAppendMonotonic_SyncFailureThenSuccessAppendsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mk := func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	}

	orig := fsyncFile
	t.Cleanup(func() { fsyncFile = orig })
	fsyncFile = func(f *os.File) error {
		return errSimulatedFsync
	}
	if _, err := AppendMonotonic(live, 0, now, mk); err == nil {
		t.Fatal("expected the first (simulated-failing) append to error")
	}

	fsyncFile = orig
	if _, err := AppendMonotonic(live, 0, now, mk); err != nil {
		t.Fatalf("retry after truncate-back must succeed: %v", err)
	}

	data, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	lines := SplitLines(data)
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 line after retry, got %d: %q", len(lines), data)
	}
}

// TestAppendMonotonic_RollbackFsyncAlsoFailsIsReported pins the leader's
// PR #509 finding I1: the truncate-back on a sync failure must itself be
// fsynced, and if that second fsync also fails, the caller must be told
// the rollback is not guaranteed durable rather than getting the same
// message as an ordinary single sync failure.
//
// What this test can and cannot prove: there is no portable way to force
// a real crash between Truncate and the filesystem's own flush and then
// inspect the file post-crash — the same limitation
// TestAppendMonotonic_SyncFailureTruncatesBack's doc comment already
// notes for the first fsync. What IS directly checkable, and what this
// test checks, is (a) the content-level rollback still happens — the
// file is empty after two simulated fsync failures, exactly as it is
// after one — and (b) the second fsync is actually attempted and its
// failure is surfaced distinctly in the returned error, rather than
// silently discarded as the pre-fix code did (pre-fix, this function
// called fsyncFile exactly once, so a fake that always errors produces
// the same single-failure message tested by
// TestAppendMonotonic_SyncFailureTruncatesBack; this test's message
// assertion is what fails against that pre-fix code).
func TestAppendMonotonic_RollbackFsyncAlsoFailsIsReported(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	orig := fsyncFile
	t.Cleanup(func() { fsyncFile = orig })
	fsyncFile = func(f *os.File) error {
		return errSimulatedFsync
	}

	_, err := AppendMonotonic(live, 0, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if err == nil {
		t.Fatal("expected an error from the simulated sync failure")
	}
	if !strings.Contains(err.Error(), "rollback not guaranteed durable across a crash") {
		t.Errorf("error must say the rollback's own fsync also failed and durability is not guaranteed, got: %v", err)
	}

	data, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("reading live file after failed append: %v", readErr)
	}
	if len(data) != 0 {
		t.Errorf("the content-level rollback must still truncate the line even when its own fsync fails, got %q", data)
	}
}

// TestAppendMonotonic_WriteFailureTruncatesBack pins the leader's PR #509
// second tail-round finding: the write-failure rollback path (a failing or
// short Write) truncates back to the pre-write length exactly like the
// sync-failure rollback path above, and both leave the file at its
// pre-append length.
//
// writeFile is a package var for the same reason fsyncFile is: a test
// cannot portably force a real Write failure.
func TestAppendMonotonic_WriteFailureTruncatesBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	orig := writeFile
	t.Cleanup(func() { writeFile = orig })
	writeFile = func(f *os.File, b []byte) (int, error) {
		return 0, errSimulatedWrite
	}

	_, err := AppendMonotonic(live, 0, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if err == nil {
		t.Fatal("expected an error from the simulated write failure")
	}

	data, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("reading live file after failed append: %v", readErr)
	}
	if len(data) != 0 {
		t.Errorf("a failed write must leave no persisted line behind, got %q", data)
	}
}

// TestAppendMonotonic_WriteFailureThenSuccessAppendsExactlyOnce is the
// end-to-end half of the write-failure fix: a retry after a write failure
// must produce exactly one line, not zero and not two.
func TestAppendMonotonic_WriteFailureThenSuccessAppendsExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mk := func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	}

	orig := writeFile
	t.Cleanup(func() { writeFile = orig })
	writeFile = func(f *os.File, b []byte) (int, error) {
		return 0, errSimulatedWrite
	}
	if _, err := AppendMonotonic(live, 0, now, mk); err == nil {
		t.Fatal("expected the first (simulated-failing) append to error")
	}

	writeFile = orig
	if _, err := AppendMonotonic(live, 0, now, mk); err != nil {
		t.Fatalf("retry after truncate-back must succeed: %v", err)
	}

	data, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	lines := SplitLines(data)
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 line after retry, got %d: %q", len(lines), data)
	}
}

// TestAppendMonotonic_WriteFailureRollbackFsyncAlsoFailsIsReported is the
// write-path counterpart to TestAppendMonotonic_RollbackFsyncAlsoFailsIsReported
// (leader's PR #509 finding: the write-failure rollback truncate was not
// itself fsynced). It pins the same two properties for the write-failure
// branch: the content-level rollback still empties the file, and a second
// fsync failure on the rollback truncate is surfaced distinctly rather than
// silently discarded.
//
// Same honest limit as the sync-path test: there is no portable way to force
// a real crash between Truncate and the filesystem's own flush and inspect
// post-crash state. This proves the second fsync is attempted and its
// failure reported, not that a crash-then-recovery round-trip is safe.
func TestAppendMonotonic_WriteFailureRollbackFsyncAlsoFailsIsReported(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	origWrite := writeFile
	t.Cleanup(func() { writeFile = origWrite })
	writeFile = func(f *os.File, b []byte) (int, error) {
		return 0, errSimulatedWrite
	}
	origSync := fsyncFile
	t.Cleanup(func() { fsyncFile = origSync })
	fsyncFile = func(f *os.File) error {
		return errSimulatedFsync
	}

	_, err := AppendMonotonic(live, 0, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if err == nil {
		t.Fatal("expected an error from the simulated write failure")
	}
	if !strings.Contains(err.Error(), "rollback not guaranteed durable across a crash") {
		t.Errorf("error must say the rollback's own fsync also failed and durability is not guaranteed, got: %v", err)
	}

	data, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("reading live file after failed append: %v", readErr)
	}
	if len(data) != 0 {
		t.Errorf("the content-level rollback must still truncate the line even when its own fsync fails, got %q", data)
	}
}

// TestAppendMonotonic_ShortWriteRollbackFsyncAlsoFailsIsReported covers the
// short-write sub-case of the write-failure branch (n < len(fullLine) with a
// nil error) — the leader flagged this as the more severe half of the
// finding, since reviving a short write resurrects a malformed partial line
// rather than a complete one. It must go through the identical rollback
// helper as a hard write error, including the second-fsync-failure report.
func TestAppendMonotonic_ShortWriteRollbackFsyncAlsoFailsIsReported(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	origWrite := writeFile
	t.Cleanup(func() { writeFile = origWrite })
	writeFile = func(f *os.File, b []byte) (int, error) {
		// Simulate a short write: write half the bytes, report no error,
		// per the io.Writer contract violation AppendMonotonic already
		// defends against.
		half := len(b) / 2
		if _, err := f.Write(b[:half]); err != nil {
			return 0, err
		}
		return half, nil
	}
	origSync := fsyncFile
	t.Cleanup(func() { fsyncFile = origSync })
	fsyncFile = func(f *os.File) error {
		return errSimulatedFsync
	}

	_, err := AppendMonotonic(live, 0, now, func(ts int64) ([]byte, error) {
		return []byte(`{"ts":"` + FormatLineTS(ts) + `"}`), nil
	})
	if err == nil {
		t.Fatal("expected an error from the simulated short write")
	}
	if !strings.Contains(err.Error(), "short write") {
		t.Errorf("error must identify the failure as a short write, got: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback not guaranteed durable across a crash") {
		t.Errorf("error must say the rollback's own fsync also failed and durability is not guaranteed, got: %v", err)
	}

	data, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("reading live file after failed append: %v", readErr)
	}
	if len(data) != 0 {
		t.Errorf("the content-level rollback must still truncate the half-written line, got %q", data)
	}
}

func TestSelectLiveTailPastWatermark(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "s.audit.jsonl")
	body := `{"ts":"` + FormatLineTS(100) + `"}` + "\n" + `{"ts":"` + FormatLineTS(200) + `"}` + "\n"
	if err := os.WriteFile(live, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	lines, first, last, err := SelectLiveTail(live, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || first != 200 || last != 200 {
		t.Errorf("tail past 100: lines=%d first=%d last=%d, want 1/200/200", len(lines), first, last)
	}
}
