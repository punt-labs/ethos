package hook

import (
	"os"
	"path/filepath"
	"testing"
)

// writeChunkFile creates a sealed chunk file holding the given ts lines.
func writeChunkFile(t *testing.T, dir, name string, tss ...int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, ts := range tss {
		line := `{"ts":"` + formatLineTS(ts) + `","session":"s","tool":"Bash"}` + "\n"
		body = append(body, line...)
	}
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionWatermarkEmptyDir(t *testing.T) {
	dir := t.TempDir()
	wm, err := sessionWatermark(filepath.Join(dir, "nope"), sessionNamespace, "", "")
	if err != nil {
		t.Fatalf("watermark on missing dir: %v", err)
	}
	if wm != 0 {
		t.Errorf("watermark = %d, want 0 for missing dir", wm)
	}
}

func TestSessionWatermarkMaxChunkLast(t *testing.T) {
	dir := t.TempDir()
	writeChunkFile(t, dir, sessionChunkFile(100, 200), 100, 200)
	writeChunkFile(t, dir, sessionChunkFile(201, 350), 201, 350)
	wm, err := sessionWatermark(dir, sessionNamespace, "", "")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 350 {
		t.Errorf("watermark = %d, want 350", wm)
	}
}

func TestSessionWatermarkLegacyContributes(t *testing.T) {
	dir := t.TempDir()
	writeChunkFile(t, dir, sessionChunkFile(100, 200), 100, 200)
	// Frozen legacy file whose max ts exceeds the chunk's last.
	legacy := filepath.Join(dir, "audit.jsonl")
	writeChunkFile(t, dir, "audit.jsonl", 50, 500, 300)
	wm, err := sessionWatermark(dir, sessionNamespace, "", legacy)
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 500 {
		t.Errorf("watermark = %d, want 500 (legacy max)", wm)
	}
}

func TestScanSealedDirNearMissFails(t *testing.T) {
	dir := t.TempDir()
	// A name carrying the chunk prefix but a short timestamp field.
	writeChunkFile(t, dir, "audit-100-200.jsonl", 100)
	_, err := scanSealedDir(dir, sessionNamespace, "")
	if err == nil {
		t.Fatal("scanSealedDir near-miss = nil error, want error")
	}
}

func TestScanSealedDirOrphanCorruptFails(t *testing.T) {
	dir := t.TempDir()
	// A .corrupt with no covering marker is a half-finished quarantine.
	writeChunkFile(t, dir, sessionChunkFile(100, 200)+".corrupt", 100, 200)
	_, err := scanSealedDir(dir, sessionNamespace, "")
	if err == nil {
		t.Fatal("scanSealedDir orphan .corrupt = nil error, want error")
	}
}

func TestScanSealedDirCorruptUnderMarkerOK(t *testing.T) {
	dir := t.TempDir()
	writeChunkFile(t, dir, sessionChunkFile(100, 200)+".corrupt", 100, 200)
	// A covering marker rescues the .corrupt from the orphan error.
	m := quarantineMarker{Chunk: "audit-" + tsToChunkField(100) + "-" + tsToChunkField(200), VerifiedLast: 200, Reason: "test"}
	data, err := marshalQuarantineMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	markerName := "audit-" + tsToChunkField(100) + "-" + tsToChunkField(200) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := scanSealedDir(dir, sessionNamespace, "")
	if err != nil {
		t.Fatalf("scanSealedDir with covering marker: %v", err)
	}
	if len(sc.markers) != 1 {
		t.Errorf("markers = %d, want 1", len(sc.markers))
	}
}

func TestScanSealedDirMissionSessionFilter(t *testing.T) {
	dir := t.TempDir()
	writeChunkFile(t, dir, missionChunkFile("sessA", 100, 200), 100, 200)
	writeChunkFile(t, dir, missionChunkFile("sessB", 300, 400), 300, 400)
	sc, err := scanSealedDir(dir, missionNamespace, "sessA")
	if err != nil {
		t.Fatalf("scanSealedDir: %v", err)
	}
	if len(sc.chunks) != 1 || sc.chunks[0].Session != "sessA" {
		t.Errorf("session filter failed: got %+v", sc.chunks)
	}
}

func TestMarkerVerifiedLastOverFilename(t *testing.T) {
	dir := t.TempDir()
	// Filename claims <last>=999 but the verified last is 200: the
	// watermark must use the verified value, not the inflated name.
	m := quarantineMarker{Chunk: "audit-" + tsToChunkField(100) + "-" + tsToChunkField(999), VerifiedLast: 200, Reason: "test"}
	data, err := marshalQuarantineMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	// Also drop the .corrupt so the marker covers it and the scan is clean.
	writeChunkFile(t, dir, "audit-"+tsToChunkField(100)+"-"+tsToChunkField(999)+".jsonl.corrupt", 100, 200)
	markerName := "audit-" + tsToChunkField(100) + "-" + tsToChunkField(999) + ".quarantine"
	if err := os.WriteFile(filepath.Join(dir, markerName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	wm, err := sessionWatermark(dir, sessionNamespace, "", "")
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	if wm != 200 {
		t.Errorf("watermark = %d, want 200 (verified, not filename 999)", wm)
	}
}

func TestReadQuarantineMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := quarantineMarker{
		Chunk:            "audit-" + tsToChunkField(100) + "-" + tsToChunkField(200),
		VerifiedLast:     150,
		UnrecoveredFirst: 151,
		UnrecoveredLast:  200,
		Reason:           "parse failure",
	}
	data, err := marshalQuarantineMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "m.quarantine")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readQuarantineMarker(path)
	if err != nil {
		t.Fatalf("readQuarantineMarker: %v", err)
	}
	if got != m {
		t.Errorf("round-trip marker = %+v, want %+v", got, m)
	}
	if !got.hasGap() {
		t.Error("hasGap() = false, want true for a recorded unrecovered range")
	}
}

func TestLivePathsShape(t *testing.T) {
	repo := "/repo"
	if got := liveAuditPath(repo, "sess1"); got != "/repo/.punt-labs/local/ethos/sessions/sess1.audit.jsonl" {
		t.Errorf("liveAuditPath = %q", got)
	}
	if got := liveAuditLockPath(repo, "sess1"); got != "/repo/.punt-labs/local/ethos/sessions/sess1.lock" {
		t.Errorf("liveAuditLockPath = %q", got)
	}
	if got := liveMissionLogPath(repo, "m-1", "sess1"); got != "/repo/.punt-labs/local/ethos/missions/m-1/sess1.log.jsonl" {
		t.Errorf("liveMissionLogPath = %q", got)
	}
	if got := sealedMissionDir(repo, "m-1"); got != "/repo/.punt-labs/ethos/missions/m-1" {
		t.Errorf("sealedMissionDir = %q", got)
	}
}
