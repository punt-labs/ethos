package enable

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func writeLiveLine(t *testing.T, root, sid string) {
	t.Helper()
	p := audit.LiveAuditPath(root, sid)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"ts\":\"2026-07-22T00:00:01.000000001Z\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Live lines under the store S count even when the invoking checkout C is a
// different directory.
func TestUnsealedAuditLinesCountsStoreLiveZone(t *testing.T) {
	store, checkout := t.TempDir(), t.TempDir()
	writeLiveLine(t, store, "sid-s")
	writeLiveLine(t, checkout, "sid-c")
	n, err := unsealedAuditLines(store, checkout)
	if err != nil {
		t.Fatalf("unsealedAuditLines: %v", err)
	}
	if n != 2 {
		t.Errorf("unsealedAuditLines = %d, want 2", n)
	}
}

// A live directory that cannot be read is an error, not zero lines.
func TestUnsealedAuditLinesReportsReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	writeLiveLine(t, root, "sid")
	dir := audit.LiveSessionsDir(root)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if n, err := unsealedAuditLines(root, root); err == nil {
		t.Errorf("unsealedAuditLines = %d, nil; want a read error", n)
	}
}
