package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

func TestCollectAuditDiagnosticsGaps(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-gap"
	now := time.Now().UTC()
	sealedDir, _ := resolveRepoSessionDir(repo, sid, now)
	// A quarantined chunk with an unrecovered sub-range.
	writeChunkFile(t, sealedDir, "audit-"+audit.TSToField(100)+"-"+audit.TSToField(300)+".jsonl.corrupt", 100, 200)
	m := audit.Marker{
		Chunk: "audit-" + audit.TSToField(100) + "-" + audit.TSToField(300), VerifiedLast: 200,
		UnrecoveredFirst: 201, UnrecoveredLast: 300, Reason: "parse failure",
	}
	data, err := audit.MarshalMarker(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealedDir, "audit-"+audit.TSToField(100)+"-"+audit.TSToField(300)+".quarantine"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	diag, err := CollectAuditDiagnostics(repo, now)
	if err != nil {
		t.Fatalf("CollectAuditDiagnostics: %v", err)
	}
	if len(diag.Gaps) != 1 || diag.Gaps[0].First != 201 {
		t.Errorf("gaps = %+v, want one [201,300]", diag.Gaps)
	}
	var buf bytes.Buffer
	diag.WriteDiagnostics(&buf)
	if !bytes.Contains(buf.Bytes(), []byte("lost lines [201,300]")) {
		t.Errorf("diagnostics output missing gap line: %q", buf.String())
	}
}

// TestCollectAuditDiagnosticsTotalLossGap is Bugbot R6-1: a quarantine that
// recovers nothing — an unparseable corrupt chunk whose range the live file no
// longer holds — must surface the whole nominal range as a gap in audit-show
// diagnostics, not report silent full recovery.
func TestCollectAuditDiagnosticsTotalLossGap(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-total-loss"
	now := time.Now().UTC()
	sealedDir, err := resolveRepoSessionDir(repo, sid, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A chunk claiming [100,300] by name but holding no parseable timestamp.
	chunk := audit.SessionChunkFile(100, 300)
	if err := os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The live file holds nothing in the chunk's range.
	live := liveAuditPath(repo, sid)
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte(`{"ts":"`+audit.FormatLineTS(50)+`","session":"`+sid+`","tool":"Read"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cn, _ := audit.Classify(chunk, audit.SessionNS)
	m, err := audit.Quarantine(repo, sealedDir, cn, live, "unparseable")
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if !m.HasGap() {
		t.Fatalf("total loss must record a gap: %+v", m)
	}

	diag, err := CollectAuditDiagnostics(repo, now)
	if err != nil {
		t.Fatalf("CollectAuditDiagnostics: %v", err)
	}
	if len(diag.Gaps) != 1 || diag.Gaps[0].First != 100 || diag.Gaps[0].Last != 300 {
		t.Errorf("gaps = %+v, want one [100,300]", diag.Gaps)
	}
	var buf bytes.Buffer
	diag.WriteDiagnostics(&buf)
	if !bytes.Contains(buf.Bytes(), []byte("lost lines [100,300]")) {
		t.Errorf("diagnostics output missing full-range gap line: %q", buf.String())
	}
}

func TestCollectAuditDiagnosticsCleanRepoNoDiag(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	writeLiveLines(t, repo, "sess-clean", 100, 200)
	diag, err := CollectAuditDiagnostics(repo, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// Not a gitlink mount and no quarantine: no gaps, no deferred, no losses.
	if len(diag.Gaps) != 0 || len(diag.Deferred) != 0 || len(diag.LossMarkers) != 0 {
		t.Errorf("clean repo diagnostics = %+v, want empty", diag)
	}
}

// TestCollectAuditDiagnosticsLossMarkers is SFH R2-3: a sentinel line in a
// session's live tail must surface in the diagnostics block, since the entry
// rendering drops the audit_error field.
func TestCollectAuditDiagnosticsLossMarkers(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-loss"
	now := time.Now().UTC()
	// A normal live line plus a sentinel line the audit writer left behind.
	writeLiveLines(t, repo, sid, 100)
	live := liveAuditPath(repo, sid)
	sentinel := `{"ts":"` + audit.FormatLineTS(200) + `","session":"` + sid + `","audit_error":"fsync failed"}` + "\n"
	f, err := os.OpenFile(live, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(sentinel); err != nil {
		t.Fatal(err)
	}
	f.Close()

	diag, err := CollectAuditDiagnostics(repo, now)
	if err != nil {
		t.Fatalf("CollectAuditDiagnostics: %v", err)
	}
	if len(diag.LossMarkers) != 1 {
		t.Fatalf("loss markers = %+v, want one", diag.LossMarkers)
	}
	if diag.LossMarkers[0].Error != "fsync failed" || diag.LossMarkers[0].Session != sid {
		t.Errorf("loss marker = %+v, want session %s error 'fsync failed'", diag.LossMarkers[0], sid)
	}
	var buf bytes.Buffer
	diag.WriteDiagnostics(&buf)
	if !bytes.Contains(buf.Bytes(), []byte("loss: session "+sid)) ||
		!bytes.Contains(buf.Bytes(), []byte("fsync failed")) {
		t.Errorf("diagnostics output missing loss line: %q", buf.String())
	}
}
