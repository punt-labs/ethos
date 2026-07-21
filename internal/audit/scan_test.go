package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

func TestWatermarkLegacyContributes(t *testing.T) {
	dir := t.TempDir()
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	legacy := filepath.Join(dir, "audit.jsonl")
	writeChunk(t, dir, "audit.jsonl", 50, 500, 300)
	wm, err := Watermark(dir, SessionNS, "", legacy)
	if err != nil || wm != 500 {
		t.Fatalf("Watermark = %d, %v; want 500 (legacy max)", wm, err)
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
