package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTemp creates a chunk temp file and stamps its mtime.
func writeTemp(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestSweepStaleTempsMissionScoping(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	ownFresh := MissionTempFile("A", 100, 200)
	ownStale := MissionTempFile("A", 300, 400)
	foreignFresh := MissionTempFile("B", 100, 200)
	foreignStale := MissionTempFile("B", 300, 400)

	writeTemp(t, dir, ownFresh, now)
	writeTemp(t, dir, ownStale, now.Add(-time.Hour))
	writeTemp(t, dir, foreignFresh, now)                                // B mid-write → must survive
	writeTemp(t, dir, foreignStale, now.Add(-staleTempAge-time.Minute)) // B crash orphan → sweep

	if err := SweepStaleTemps(dir, MissionNS, "A", now); err != nil {
		t.Fatal(err)
	}

	if exists(dir, ownFresh) {
		t.Error("own session's temp must be swept regardless of age (fresh)")
	}
	if exists(dir, ownStale) {
		t.Error("own session's temp must be swept regardless of age (stale)")
	}
	if !exists(dir, foreignFresh) {
		t.Error("a foreign session's in-flight temp must survive the sweep")
	}
	if exists(dir, foreignStale) {
		t.Error("a foreign session's crash-orphaned temp must be swept")
	}
}

func TestSweepStaleTempsSessionNamespaceSweepsAll(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	fresh := SessionTempFile(100, 200)
	writeTemp(t, dir, fresh, now)
	// The session dir is single-session, so every temp is this session's.
	if err := SweepStaleTemps(dir, SessionNS, "", now); err != nil {
		t.Fatal(err)
	}
	if exists(dir, fresh) {
		t.Error("session-namespace sweep must remove all temps")
	}
}

func TestSweepStaleTempsMissingDir(t *testing.T) {
	if err := SweepStaleTemps(filepath.Join(t.TempDir(), "nope"), MissionNS, "A", time.Now()); err != nil {
		t.Fatalf("missing dir must be a no-op, got %v", err)
	}
}
